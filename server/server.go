package server

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/kamal-hamza/lx-cli/pkg/vault"
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

// parseNoteHeader extracts metadata from a note file
func (s *LanguageServer) parseNoteHeader(filename string) (*NoteHeader, error) {
	path := s.vault.GetNotePath(filename)
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	header := &NoteHeader{
		Filename: filename,
		Slug:     s.parseFilenameToSlug(filename),
		Tags:     []string{},
	}

	text := string(content)

	// Parse title from % title: format
	titleRe := regexp.MustCompile(`(?m)^%+\s*title:\s*(.+)$`)
	if matches := titleRe.FindStringSubmatch(text); len(matches) > 1 {
		header.Title = strings.TrimSpace(matches[1])
	}

	// Parse date from % date: format
	dateRe := regexp.MustCompile(`(?m)^%+\s*date:\s*(.+)$`)
	if matches := dateRe.FindStringSubmatch(text); len(matches) > 1 {
		header.Date = strings.TrimSpace(matches[1])
	}

	// Parse tags from % tags: format (comma-separated)
	tagsRe := regexp.MustCompile(`(?m)^%+\s*tags:\s*(.*)$`)
	if matches := tagsRe.FindStringSubmatch(text); len(matches) > 1 {
		tagsStr := strings.TrimSpace(matches[1])
		if tagsStr != "" {
			tags := strings.Split(tagsStr, ",")
			for _, tag := range tags {
				trimmed := strings.TrimSpace(tag)
				if trimmed != "" {
					header.Tags = append(header.Tags, trimmed)
				}
			}
		}
	}

	// If no title found, use filename
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

	// Find first hyphen (after date)
	parts := strings.SplitN(name, "-", 2)
	if len(parts) == 2 {
		return parts[1]
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
