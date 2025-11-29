package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"go.lsp.dev/protocol"
)

// Handle Initialize request
func (s *LanguageServer) Initialize(ctx context.Context, params *protocol.InitializeParams) (*protocol.InitializeResult, error) {
	return &protocol.InitializeResult{
		Capabilities: protocol.ServerCapabilities{
			TextDocumentSync: protocol.TextDocumentSyncOptions{
				OpenClose: true,
				Change:    protocol.TextDocumentSyncKindFull,
			},
			CompletionProvider: &protocol.CompletionOptions{
				TriggerCharacters: []string{"{", "\\", "a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p", "q", "r", "s", "t", "u", "v", "w", "x", "y", "z", "-"},
			},
			DefinitionProvider: true,
			HoverProvider:      true,
			RenameProvider:     true,
			DocumentLinkProvider: &protocol.DocumentLinkOptions{
				ResolveProvider: false,
			},
		},
		ServerInfo: &protocol.ServerInfo{
			Name:    "lx-ls",
			Version: "0.1.1",
		},
	}, nil
}

// Handle Rename request
func (s *LanguageServer) Rename(ctx context.Context, params *protocol.RenameParams) (*protocol.WorkspaceEdit, error) {
	if !s.IsManaged(params.TextDocument.URI) {
		return nil, nil
	}

	content, err := s.GetDocument(params.TextDocument.URI)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	oldSlug := s.getSlugAtPosition(content, params.Position)
	if oldSlug == "" {
		return nil, fmt.Errorf("no valid note reference found at cursor")
	}

	newTitle := params.NewName

	// Shell out to LX CLI
	cmd := exec.Command("lx", "rename", oldSlug, newTitle)
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("lx rename failed: %s", string(output))
	}

	// Return nil edit so editor reloads from disk
	return &protocol.WorkspaceEdit{}, nil
}

// Handle DidOpen notification
func (s *LanguageServer) DidOpen(ctx context.Context, params *protocol.DidOpenTextDocumentParams) error {
	if !s.IsManaged(params.TextDocument.URI) {
		return nil
	}

	// Store document in memory
	s.mu.Lock()
	s.documents[params.TextDocument.URI] = params.TextDocument.Text
	s.mu.Unlock()

	// Run diagnostics
	return s.publishDiagnostics(ctx, params.TextDocument.URI, params.TextDocument.Text)
}

// Handle DidChange notification
func (s *LanguageServer) DidChange(ctx context.Context, params *protocol.DidChangeTextDocumentParams) error {
	if !s.IsManaged(params.TextDocument.URI) {
		return nil
	}

	if len(params.ContentChanges) == 0 {
		return nil
	}

	text := params.ContentChanges[0].Text

	// Update document in memory
	s.mu.Lock()
	s.documents[params.TextDocument.URI] = text
	s.mu.Unlock()

	// Run diagnostics
	return s.publishDiagnostics(ctx, params.TextDocument.URI, text)
}

// Handle DidClose notification
func (s *LanguageServer) DidClose(ctx context.Context, params *protocol.DidCloseTextDocumentParams) error {
	// Remove from memory to prevent leaks
	s.mu.Lock()
	delete(s.documents, params.TextDocument.URI)
	s.mu.Unlock()
	return nil
}

// Handle Completion request
func (s *LanguageServer) Completion(ctx context.Context, params *protocol.CompletionParams) (*protocol.CompletionList, error) {
	if !s.IsManaged(params.TextDocument.URI) {
		return &protocol.CompletionList{Items: []protocol.CompletionItem{}}, nil
	}

	// Read content from memory (this now includes the just-typed '{')
	content, err := s.GetDocument(params.TextDocument.URI)
	if err != nil {
		return &protocol.CompletionList{Items: []protocol.CompletionItem{}}, nil
	}

	lines := strings.Split(content, "\n")
	if int(params.Position.Line) >= len(lines) {
		return &protocol.CompletionList{Items: []protocol.CompletionItem{}}, nil
	}

	line := lines[params.Position.Line]
	if int(params.Position.Character) > len(line) {
		return &protocol.CompletionList{Items: []protocol.CompletionItem{}}, nil
	}

	linePrefix := line[:params.Position.Character]

	var items []protocol.CompletionItem

	// Check if we're inside \ref{...}
	refPattern := regexp.MustCompile(`\\ref\{([^}]*)$`)
	if matches := refPattern.FindStringSubmatch(linePrefix); matches != nil {
		prefix := matches[1]
		items = s.getRefCompletions()

		// Filter completions based on what's already typed
		if prefix != "" {
			filtered := []protocol.CompletionItem{}
			for _, item := range items {
				if strings.HasPrefix(item.Label, prefix) {
					filtered = append(filtered, item)
				}
			}
			items = filtered
		}
	}

	// Check if we're inside \usepackage{...}
	pkgPattern := regexp.MustCompile(`\\usepackage\{([^}]*)$`)
	if matches := pkgPattern.FindStringSubmatch(linePrefix); matches != nil {
		prefix := matches[1]
		templateItems := s.getTemplateCompletions()

		if prefix != "" {
			filtered := []protocol.CompletionItem{}
			for _, item := range templateItems {
				if strings.HasPrefix(item.Label, prefix) {
					filtered = append(filtered, item)
				}
			}
			items = append(items, filtered...)
		} else {
			items = append(items, templateItems...)
		}
	}

	// Add custom snippets when not inside a completion context
	if len(items) == 0 {
		items = append(items, s.getSnippetCompletions()...)
	}

	return &protocol.CompletionList{
		IsIncomplete: false,
		Items:        items,
	}, nil
}

// getRefCompletions returns completions for note references
func (s *LanguageServer) getRefCompletions() []protocol.CompletionItem {
	notes := s.index.All()
	items := make([]protocol.CompletionItem, 0, len(notes))

	for _, note := range notes {
		items = append(items, protocol.CompletionItem{
			Label:      note.Slug,
			Kind:       protocol.CompletionItemKindReference,
			Detail:     note.Title,
			InsertText: note.Slug,
		})
	}

	return items
}

// getTemplateCompletions returns completions for templates
func (s *LanguageServer) getTemplateCompletions() []protocol.CompletionItem {
	templates, err := s.listTemplates()
	if err != nil {
		return []protocol.CompletionItem{}
	}

	items := make([]protocol.CompletionItem, 0, len(templates))
	for _, tmpl := range templates {
		items = append(items, protocol.CompletionItem{
			Label:      tmpl,
			Kind:       protocol.CompletionItemKindModule,
			InsertText: tmpl,
		})
	}

	return items
}

// listTemplates returns all available template names
func (s *LanguageServer) listTemplates() ([]string, error) {
	entries, err := os.ReadDir(s.vault.TemplatesPath)
	if err != nil {
		return nil, err
	}

	var templates []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sty") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".sty")
		templates = append(templates, name)
	}

	return templates, nil
}

// getSnippetCompletions returns custom LX snippets
func (s *LanguageServer) getSnippetCompletions() []protocol.CompletionItem {
	return []protocol.CompletionItem{
		{
			Label:      "\\todo{}",
			Kind:       protocol.CompletionItemKindSnippet,
			Detail:     "TODO marker",
			InsertText: "\\todo{${1:description}}",
		},
		{
			Label:      "\\includegraphics",
			Kind:       protocol.CompletionItemKindSnippet,
			Detail:     "Include asset",
			InsertText: "\\includegraphics[width=0.8\\linewidth]{${1:filename}}",
		},
	}
}

// Handle Definition request (Go to Definition)
func (s *LanguageServer) Definition(ctx context.Context, params *protocol.DefinitionParams) ([]protocol.Location, error) {
	if !s.IsManaged(params.TextDocument.URI) {
		return nil, nil
	}

	content, err := s.GetDocument(params.TextDocument.URI)
	if err != nil {
		return nil, nil
	}

	slug := s.getSlugAtPosition(content, params.Position)
	if slug == "" {
		return nil, nil
	}

	note, exists := s.index.Get(slug)
	if !exists {
		return nil, nil
	}

	notePath := s.vault.GetNotePath(note.Filename)
	uri := protocol.DocumentURI("file://" + notePath)

	return []protocol.Location{
		{
			URI: uri,
			Range: protocol.Range{
				Start: protocol.Position{Line: 0, Character: 0},
				End:   protocol.Position{Line: 0, Character: 0},
			},
		},
	}, nil
}

// Handle Hover request
func (s *LanguageServer) Hover(ctx context.Context, params *protocol.HoverParams) (*protocol.Hover, error) {
	if !s.IsManaged(params.TextDocument.URI) {
		return nil, nil
	}

	content, err := s.GetDocument(params.TextDocument.URI)
	if err != nil {
		return nil, nil
	}

	slug := s.getSlugAtPosition(content, params.Position)
	if slug == "" {
		return nil, nil
	}

	note, exists := s.index.Get(slug)
	if !exists {
		return nil, nil
	}

	hoverText := fmt.Sprintf("**%s**\n\nSlug: `%s`\nDate: %s",
		note.Title,
		note.Slug,
		note.Date,
	)

	if len(note.Tags) > 0 {
		hoverText += fmt.Sprintf("\nTags: %s", strings.Join(note.Tags, ", "))
	}

	return &protocol.Hover{
		Contents: protocol.MarkupContent{
			Kind:  protocol.Markdown,
			Value: hoverText,
		},
	}, nil
}

// getSlugAtPosition extracts a slug from the given position
func (s *LanguageServer) getSlugAtPosition(content string, pos protocol.Position) string {
	lines := strings.Split(content, "\n")
	if int(pos.Line) >= len(lines) {
		return ""
	}

	line := lines[pos.Line]

	// Find \ref{slug} or similar patterns
	refPattern := regexp.MustCompile(`\\(?:ref|cite|input|include)\{([^}]+)\}`)
	matches := refPattern.FindAllStringSubmatchIndex(line, -1)

	for _, match := range matches {
		if int(pos.Character) >= match[2] && int(pos.Character) <= match[3] {
			rawSlug := line[match[2]:match[3]]
			// Normalize
			slug := strings.TrimSpace(rawSlug)
			slug = strings.TrimSuffix(slug, ".tex")
			slug = strings.TrimPrefix(slug, "../notes/")
			return slug
		}
	}

	return ""
}

// publishDiagnostics analyzes content and publishes diagnostics
func (s *LanguageServer) publishDiagnostics(ctx context.Context, uri protocol.DocumentURI, content string) error {
	diagnostics := s.analyzeDiagnostics(content)

	return s.conn.Notify(ctx, protocol.MethodTextDocumentPublishDiagnostics, &protocol.PublishDiagnosticsParams{
		URI:         uri,
		Diagnostics: diagnostics,
	})
}

// analyzeDiagnostics scans content for issues
func (s *LanguageServer) analyzeDiagnostics(content string) []protocol.Diagnostic {
	var diagnostics []protocol.Diagnostic

	lines := strings.Split(content, "\n")
	refPattern := regexp.MustCompile(`\\(?:ref|cite)\{([^}]+)\}`)
	todoPattern := regexp.MustCompile(`\\todo\{([^}]+)\}`)

	for lineNum, line := range lines {
		// Skip comment lines
		if strings.HasPrefix(strings.TrimSpace(line), "%") {
			continue
		}

		// Check for broken note references
		refMatches := refPattern.FindAllStringSubmatchIndex(line, -1)
		for _, match := range refMatches {
			rawSlug := line[match[2]:match[3]]
			slug := strings.TrimSpace(rawSlug)
			slug = strings.TrimSuffix(slug, ".tex")

			if _, exists := s.index.Get(slug); !exists {
				diagnostics = append(diagnostics, protocol.Diagnostic{
					Range: protocol.Range{
						Start: protocol.Position{Line: uint32(lineNum), Character: uint32(match[2])},
						End:   protocol.Position{Line: uint32(lineNum), Character: uint32(match[3])},
					},
					Severity: protocol.DiagnosticSeverityError,
					Message:  fmt.Sprintf("Note '%s' not found", slug),
					Source:   "lx-ls",
				})
			}
		}

		// Check for TODOs
		todoMatches := todoPattern.FindAllStringSubmatchIndex(line, -1)
		for _, match := range todoMatches {
			todoText := line[match[2]:match[3]]
			diagnostics = append(diagnostics, protocol.Diagnostic{
				Range: protocol.Range{
					Start: protocol.Position{Line: uint32(lineNum), Character: uint32(match[0])},
					End:   protocol.Position{Line: uint32(lineNum), Character: uint32(match[1])},
				},
				Severity: protocol.DiagnosticSeverityWarning,
				Message:  fmt.Sprintf("TODO: %s", todoText),
				Source:   "lx-ls",
			})
		}
	}

	return diagnostics
}
