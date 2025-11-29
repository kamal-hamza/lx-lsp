package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/kamal-hamza/lx-cli/pkg/vault"
	"github.com/kamal-hamza/lx-lsp/pkg/metadata"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

// NoteHeader represents a note's metadata
type NoteHeader struct {
	Title    string
	Date     string
	Tags     []string
	Slug     string
	Filename string
}

type LanguageServer struct {
	vault   *vault.Vault
	index   *Index
	conn    jsonrpc2.Conn
	watcher *fsnotify.Watcher // <--- Added Watcher
	mu      sync.RWMutex
}

type Index struct {
	mu    sync.RWMutex
	notes map[string]*NoteHeader // slug -> header
}

func NewIndex() *Index {
	return &Index{
		notes: make(map[string]*NoteHeader),
	}
}

func (i *Index) Get(slug string) (*NoteHeader, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	note, exists := i.notes[slug]
	return note, exists
}

func (i *Index) Set(slug string, header *NoteHeader) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.notes[slug] = header
}

func (i *Index) Delete(slug string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.notes, slug)
}

func (i *Index) Count() int {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return len(i.notes)
}

func (i *Index) All() []*NoteHeader {
	i.mu.RLock()
	defer i.mu.RUnlock()
	notes := make([]*NoteHeader, 0, len(i.notes))
	for _, note := range i.notes {
		notes = append(notes, note)
	}
	return notes
}

func NewLanguageServer() (*LanguageServer, error) {
	// Initialize vault
	v, err := vault.New()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize vault: %w", err)
	}

	// Check if vault exists
	if !v.Exists() {
		return nil, fmt.Errorf("vault not initialized at %s", v.RootPath)
	}

	return &LanguageServer{
		vault: v,
		index: NewIndex(),
	}, nil
}

func (s *LanguageServer) Run(ctx context.Context) error {
	// Set up JSON-RPC connection over stdio
	stream := jsonrpc2.NewStream(
		struct {
			io.Reader
			io.WriteCloser
		}{os.Stdin, os.Stdout},
	)

	conn := jsonrpc2.NewConn(stream)
	conn.Go(ctx, s.handler())
	s.conn = conn

	// Build initial index
	if err := s.RebuildIndex(ctx); err != nil {
		return fmt.Errorf("failed to build initial index: %w", err)
	}

	// --- Start File Watcher ---
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create watcher: %w", err)
	}
	s.watcher = watcher
	defer s.watcher.Close()

	// Watch Notes directory
	if err := s.watcher.Add(s.vault.NotesPath); err != nil {
		return fmt.Errorf("failed to watch notes directory: %w", err)
	}

	// Handle events in background
	go s.handleFileEvents(ctx)
	// --------------------------

	// Wait for connection to close
	<-conn.Done()
	return conn.Err()
}

// handleFileEvents watches for changes in the notes directory
func (s *LanguageServer) handleFileEvents(ctx context.Context) {
	for {
		select {
		case event, ok := <-s.watcher.Events:
			if !ok {
				return
			}
			// Only care about .tex files
			if strings.HasSuffix(event.Name, ".tex") {
				// Update index for this specific file
				s.updateIndexForFile(event.Name)
			}
		case <-ctx.Done():
			return
		}
	}
}

// updateIndexForFile updates a single entry in the index
func (s *LanguageServer) updateIndexForFile(path string) {
	// 1. Check if file was deleted
	if _, err := os.Stat(path); os.IsNotExist(err) {
		slug := s.parseFilenameToSlug(filepath.Base(path))
		s.index.Delete(slug)
		return
	}

	// 2. Parse and Update
	header, err := s.parseNoteHeader(filepath.Base(path))
	if err == nil {
		s.index.Set(header.Slug, header)
	}
}

// RebuildIndex scans all notes and rebuilds the index
func (s *LanguageServer) RebuildIndex(ctx context.Context) error {
	headers, err := s.listNoteHeaders(ctx)
	if err != nil {
		return err
	}

	for _, header := range headers {
		s.index.Set(header.Slug, header)
	}

	return nil
}

// listNoteHeaders reads all .tex files in notes directory and parses metadata
func (s *LanguageServer) listNoteHeaders(ctx context.Context) ([]*NoteHeader, error) {
	var headers []*NoteHeader

	entries, err := os.ReadDir(s.vault.NotesPath)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tex") {
			continue
		}

		header, err := s.parseNoteHeader(entry.Name())
		if err != nil {
			continue // Skip malformed files
		}

		headers = append(headers, header)
	}

	return headers, nil
}

// parseNoteHeader extracts metadata from a note file using robust metadata parser
func (s *LanguageServer) parseNoteHeader(filename string) (*NoteHeader, error) {
	path := s.vault.GetNotePath(filename)
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Use non-strict parser for reading existing files
	// This allows recovery from minor metadata issues
	meta, err := metadata.Extract(string(content))
	if err != nil {
		// Fallback: create minimal header from filename
		slug := s.parseFilenameToSlug(filename)
		return &NoteHeader{
			Filename: filename,
			Slug:     slug,
			Title:    slug,
			Date:     "",
			Tags:     []string{},
		}, nil
	}

	header := &NoteHeader{
		Filename: filename,
		Slug:     s.parseFilenameToSlug(filename),
		Title:    meta.Title,
		Date:     meta.Date,
		Tags:     meta.Tags,
	}

	// Ensure tags is never nil
	if header.Tags == nil {
		header.Tags = []string{}
	}

	// If no title found, use slug as fallback
	if header.Title == "" {
		header.Title = header.Slug
	}

	return header, nil
}

// parseFilenameToSlug extracts slug from filename
// "20251128-graph-theory.tex" -> "graph-theory"
func (s *LanguageServer) parseFilenameToSlug(filename string) string {
	// Remove .tex extension
	name := strings.TrimSuffix(filename, ".tex")

	// Check if filename has date prefix (YYYYMMDD-slug format)
	parts := strings.SplitN(name, "-", 2)
	if len(parts) == 2 && len(parts[0]) == 8 {
		// Verify first part is all digits (a date)
		allDigits := true
		for _, ch := range parts[0] {
			if ch < '0' || ch > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return parts[1]
		}
	}

	return name
}

// IsManaged checks if a file URI is a managed note in the vault
func (s *LanguageServer) IsManaged(uri protocol.DocumentURI) bool {
	// Convert URI to file path
	path := uriToPath(uri)
	if path == "" {
		return false
	}

	// Check if path is within vault notes directory
	notesPath, err := filepath.Abs(s.vault.NotesPath)
	if err != nil {
		return false
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}

	// Must be .tex file in notes directory
	if !strings.HasSuffix(absPath, ".tex") {
		return false
	}

	return strings.HasPrefix(absPath, notesPath)
}

// uriToPath converts a URI to a file path
func uriToPath(uri protocol.DocumentURI) string {
	path := string(uri)
	// Remove file:// prefix
	if strings.TrimPrefix(path, "file://") != "" {
		path = path[7:]
	}
	return path
}

// handler returns the JSON-RPC handler for LSP methods
func (s *LanguageServer) handler() jsonrpc2.Handler {
	return func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		switch req.Method() {
		case protocol.MethodInitialize:
			var params protocol.InitializeParams
			if err := json.Unmarshal(req.Params(), &params); err != nil {
				return reply(ctx, nil, err)
			}
			result, err := s.Initialize(ctx, &params)
			return reply(ctx, result, err)

		case protocol.MethodInitialized:
			return reply(ctx, nil, nil)

		case protocol.MethodTextDocumentDidOpen:
			var params protocol.DidOpenTextDocumentParams
			if err := json.Unmarshal(req.Params(), &params); err != nil {
				return reply(ctx, nil, err)
			}
			err := s.DidOpen(ctx, &params)
			return reply(ctx, nil, err)

		case protocol.MethodTextDocumentDidChange:
			var params protocol.DidChangeTextDocumentParams
			if err := json.Unmarshal(req.Params(), &params); err != nil {
				return reply(ctx, nil, err)
			}
			err := s.DidChange(ctx, &params)
			return reply(ctx, nil, err)

		case protocol.MethodTextDocumentCompletion:
			var params protocol.CompletionParams
			if err := json.Unmarshal(req.Params(), &params); err != nil {
				return reply(ctx, nil, err)
			}
			result, err := s.Completion(ctx, &params)
			return reply(ctx, result, err)

		case protocol.MethodTextDocumentDefinition:
			var params protocol.DefinitionParams
			if err := json.Unmarshal(req.Params(), &params); err != nil {
				return reply(ctx, nil, err)
			}
			result, err := s.Definition(ctx, &params)
			return reply(ctx, result, err)

		case protocol.MethodTextDocumentHover:
			var params protocol.HoverParams
			if err := json.Unmarshal(req.Params(), &params); err != nil {
				return reply(ctx, nil, err)
			}
			result, err := s.Hover(ctx, &params)
			return reply(ctx, result, err)

		case protocol.MethodTextDocumentRename:
			var params protocol.RenameParams
			if err := json.Unmarshal(req.Params(), &params); err != nil {
				return reply(ctx, nil, err)
			}
			result, err := s.Rename(ctx, &params)
			return reply(ctx, result, err)

		case protocol.MethodShutdown:
			return reply(ctx, nil, nil)

		case protocol.MethodExit:
			return nil

		default:
			return reply(ctx, nil, jsonrpc2.ErrMethodNotFound)
		}
	}
}
