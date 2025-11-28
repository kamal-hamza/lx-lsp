package server

import (
	"context"
	"fmt"
	"os"
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
				TriggerCharacters: []string{"{", "\\"},
			},
			DefinitionProvider: true,
			HoverProvider:      true,
			DocumentLinkProvider: &protocol.DocumentLinkOptions{
				ResolveProvider: false,
			},
		},
		ServerInfo: &protocol.ServerInfo{
			Name:    "lx-ls",
			Version: "0.1.0",
		},
	}, nil
}

// Handle DidOpen notification
func (s *LanguageServer) DidOpen(ctx context.Context, params *protocol.DidOpenTextDocumentParams) error {
	if !s.IsManaged(params.TextDocument.URI) {
		return nil
	}

	// Run diagnostics
	return s.publishDiagnostics(ctx, params.TextDocument.URI, params.TextDocument.Text)
}

// Handle DidChange notification
func (s *LanguageServer) DidChange(ctx context.Context, params *protocol.DidChangeTextDocumentParams) error {
	if !s.IsManaged(params.TextDocument.URI) {
		return nil
	}

	// Get the latest content
	if len(params.ContentChanges) == 0 {
		return nil
	}

	text := params.ContentChanges[0].Text

	// Run diagnostics
	return s.publishDiagnostics(ctx, params.TextDocument.URI, text)
}

// Handle Completion request
func (s *LanguageServer) Completion(ctx context.Context, params *protocol.CompletionParams) (*protocol.CompletionList, error) {
	if !s.IsManaged(params.TextDocument.URI) {
		return &protocol.CompletionList{Items: []protocol.CompletionItem{}}, nil
	}

	// Read current document
	path := uriToPath(params.TextDocument.URI)
	content, err := os.ReadFile(path)
	if err != nil {
		return &protocol.CompletionList{Items: []protocol.CompletionItem{}}, nil
	}

	lines := strings.Split(string(content), "\n")
	if int(params.Position.Line) >= len(lines) {
		return &protocol.CompletionList{Items: []protocol.CompletionItem{}}, nil
	}

	line := lines[params.Position.Line]
	linePrefix := line[:params.Position.Character]

	var items []protocol.CompletionItem

	// Check for \ref{ trigger
	if strings.HasSuffix(linePrefix, "\\ref{") {
		items = s.getRefCompletions()
	}

	// Check for \usepackage{ trigger (templates)
	if strings.HasSuffix(linePrefix, "\\usepackage{") {
		items = s.getTemplateCompletions()
	}

	// Add custom snippets
	items = append(items, s.getSnippetCompletions()...)

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

	// Read document and find slug at position
	path := uriToPath(params.TextDocument.URI)
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}

	slug := s.getSlugAtPosition(string(content), params.Position)
	if slug == "" {
		return nil, nil
	}

	// Look up note in index
	note, exists := s.index.Get(slug)
	if !exists {
		return nil, nil
	}

	// Return location of the note file
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

	// Read document and find slug at position
	path := uriToPath(params.TextDocument.URI)
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}

	slug := s.getSlugAtPosition(string(content), params.Position)
	if slug == "" {
		return nil, nil
	}

	// Look up note in index
	note, exists := s.index.Get(slug)
	if !exists {
		return nil, nil
	}

	// Build markdown hover content
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
		// match[2] and match[3] are the start and end of the captured group
		if int(pos.Character) >= match[2] && int(pos.Character) <= match[3] {
			slug := line[match[2]:match[3]]
			// Normalize slug (remove .tex, paths, etc.)
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

	// Pattern for \ref{slug}
	refPattern := regexp.MustCompile(`\\(?:ref|cite)\{([^}]+)\}`)

	// Pattern for \todo{text}
	todoPattern := regexp.MustCompile(`\\todo\{([^}]+)\}`)

	for lineNum, line := range lines {
		// Skip comment lines
		if strings.HasPrefix(strings.TrimSpace(line), "%") {
			continue
		}

		// Check for broken links
		refMatches := refPattern.FindAllStringSubmatchIndex(line, -1)
		for _, match := range refMatches {
			slug := line[match[2]:match[3]]
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
