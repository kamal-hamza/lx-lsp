package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kamal-hamza/lx-cli/pkg/vault"
	"go.lsp.dev/protocol"
)

// TestNewLanguageServer_VaultDiscovery tests vault initialization
func TestNewLanguageServer_VaultDiscovery(t *testing.T) {
	// Setup: Create a temp vault structure
	tempDir := t.TempDir()

	// Override vault path for testing
	os.Setenv("XDG_DATA_HOME", tempDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	v, err := vault.New()
	if err != nil {
		t.Fatalf("failed to create vault: %v", err)
	}

	if err := v.Initialize(); err != nil {
		t.Fatalf("failed to initialize vault: %v", err)
	}

	// Action: Initialize the LS
	ls, err := NewLanguageServer()

	// Assert
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if ls.vault == nil {
		t.Error("expected vault to be initialized")
	}

	if ls.vault.RootPath == "" {
		t.Error("expected vault root path to be set")
	}
}

// TestIsNoteManaged tests the gatekeeper function
func TestIsNoteManaged(t *testing.T) {
	// Setup
	tempDir := t.TempDir()
	notesPath := filepath.Join(tempDir, "lx", "notes")
	templatesPath := filepath.Join(tempDir, "lx", "templates")

	os.MkdirAll(notesPath, 0755)
	os.MkdirAll(templatesPath, 0755)

	ls := &LanguageServer{
		vault: &vault.Vault{
			RootPath:      filepath.Join(tempDir, "lx"),
			NotesPath:     notesPath,
			TemplatesPath: templatesPath,
		},
	}

	tests := []struct {
		name string
		uri  protocol.DocumentURI
		want bool
	}{
		{
			name: "Valid Note",
			uri:  protocol.DocumentURI("file://" + filepath.Join(notesPath, "graph.tex")),
			want: true,
		},
		{
			name: "Random Tex",
			uri:  protocol.DocumentURI("file:///tmp/homework.tex"),
			want: false,
		},
		{
			name: "Template",
			uri:  protocol.DocumentURI("file://" + filepath.Join(templatesPath, "base.sty")),
			want: false,
		},
		{
			name: "Non-tex file in notes",
			uri:  protocol.DocumentURI("file://" + filepath.Join(notesPath, "image.png")),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ls.IsManaged(tt.uri)
			if got != tt.want {
				t.Errorf("IsManaged() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestBuildIndex tests initial index construction
func TestBuildIndex(t *testing.T) {
	// Setup: Create mock vault with test notes
	tempDir := t.TempDir()
	notesPath := filepath.Join(tempDir, "notes")
	os.MkdirAll(notesPath, 0755)

	// Create test notes
	testNotes := []struct {
		filename string
		content  string
	}{
		{
			"20240101-graph-theory.tex",
			"%% title: Graph Theory\n%% date: 2024-01-01\n%% tags: math\n\\documentclass{article}\n\\begin{document}\nContent\n\\end{document}",
		},
		{
			"20240102-linear-algebra.tex",
			"%% title: Linear Algebra\n%% date: 2024-01-02\n%% tags: math\n\\documentclass{article}\n\\begin{document}\nContent\n\\end{document}",
		},
	}

	for _, note := range testNotes {
		path := filepath.Join(notesPath, note.filename)
		if err := os.WriteFile(path, []byte(note.content), 0644); err != nil {
			t.Fatalf("failed to create test note: %v", err)
		}
	}

	v := &vault.Vault{
		RootPath:  tempDir,
		NotesPath: notesPath,
	}

	ls, err := NewLanguageServer()
	if err != nil {
		// Expected if vault not in standard location
		// Create LS manually for test
		ls = &LanguageServer{
			vault: v,
			index: NewIndex(),
		}
	}

	// Action: Build index
	err = ls.RebuildIndex(context.Background())

	// Assert
	if err != nil {
		t.Fatalf("RebuildIndex failed: %v", err)
	}

	if ls.index.Count() != 2 {
		t.Errorf("expected 2 notes in index, got %d", ls.index.Count())
	}

	note, exists := ls.index.Get("graph-theory")
	if !exists {
		t.Error("expected 'graph-theory' in index")
	}
	if note != nil && note.Title != "Graph Theory" {
		t.Errorf("expected title 'Graph Theory', got '%s'", note.Title)
	}
}

// TestCompletion_References tests reference completion
func TestCompletion_References(t *testing.T) {
	ls := &LanguageServer{
		index: NewIndex(),
	}

	// Add test notes to index
	ls.index.Set("graph-theory", &NoteHeader{
		Title: "Graph Theory",
		Slug:  "graph-theory",
	})
	ls.index.Set("linear-algebra", &NoteHeader{
		Title: "Linear Algebra",
		Slug:  "linear-algebra",
	})

	tests := []struct {
		name      string
		line      string
		character uint32
		wantItems int
	}{
		{
			name:      "After \\ref{",
			line:      "See \\ref{",
			character: 9,
			wantItems: 2, // Should suggest both notes
		},
		{
			name:      "Not a trigger",
			line:      "Normal text",
			character: 5,
			wantItems: 2, // Snippets only
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp file with test content
			tempDir := t.TempDir()
			notesPath := filepath.Join(tempDir, "notes")
			os.MkdirAll(notesPath, 0755)

			testFile := filepath.Join(notesPath, "test.tex")
			os.WriteFile(testFile, []byte(tt.line), 0644)

			ls.vault = &vault.Vault{NotesPath: notesPath}

			params := &protocol.CompletionParams{
				TextDocumentPositionParams: protocol.TextDocumentPositionParams{
					TextDocument: protocol.TextDocumentIdentifier{
						URI: protocol.DocumentURI("file://" + testFile),
					},
					Position: protocol.Position{
						Line:      0,
						Character: tt.character,
					},
				},
			}

			result, err := ls.Completion(context.Background(), params)
			if err != nil {
				t.Fatalf("Completion failed: %v", err)
			}

			if len(result.Items) < tt.wantItems {
				t.Errorf("expected at least %d completion items, got %d", tt.wantItems, len(result.Items))
			}
		})
	}
}

// TestDiagnostics_BrokenLinks tests broken link detection
func TestDiagnostics_BrokenLinks(t *testing.T) {
	ls := &LanguageServer{
		index: NewIndex(),
	}

	// Add only one note to index
	ls.index.Set("existing-note", &NoteHeader{
		Title: "Existing Note",
		Slug:  "existing-note",
	})

	content := `
Check \ref{existing-note}.
See \ref{missing-note}.
`

	diagnostics := ls.analyzeDiagnostics(content)

	// Should find 1 error (missing-note)
	errorCount := 0
	for _, diag := range diagnostics {
		if diag.Severity == protocol.DiagnosticSeverityError {
			errorCount++
			if !strings.Contains(diag.Message, "missing-note") {
				t.Errorf("expected error for 'missing-note', got: %s", diag.Message)
			}
		}
	}

	if errorCount != 1 {
		t.Errorf("expected 1 error diagnostic, got %d", errorCount)
	}
}

// TestDiagnostics_Todos tests TODO detection
func TestDiagnostics_Todos(t *testing.T) {
	ls := &LanguageServer{
		index: NewIndex(),
	}

	content := `\todo{Fix this paragraph}`

	diagnostics := ls.analyzeDiagnostics(content)

	// Should find 1 warning
	warningCount := 0
	for _, diag := range diagnostics {
		if diag.Severity == protocol.DiagnosticSeverityWarning {
			warningCount++
			if !strings.Contains(diag.Message, "TODO") {
				t.Errorf("expected TODO warning, got: %s", diag.Message)
			}
		}
	}

	if warningCount != 1 {
		t.Errorf("expected 1 warning diagnostic, got %d", warningCount)
	}
}

// TestDiagnostics_IgnoreComments tests comment handling
func TestDiagnostics_IgnoreComments(t *testing.T) {
	ls := &LanguageServer{
		index: NewIndex(),
	}

	content := `% \ref{broken-link} - this should be ignored`

	diagnostics := ls.analyzeDiagnostics(content)

	// Should find 0 diagnostics
	if len(diagnostics) != 0 {
		t.Errorf("expected 0 diagnostics for commented line, got %d", len(diagnostics))
	}
}

// TestDefinition tests go-to-definition
func TestDefinition(t *testing.T) {
	tempDir := t.TempDir()
	notesPath := filepath.Join(tempDir, "notes")
	os.MkdirAll(notesPath, 0755)

	// Create target note file
	targetFile := "20240101-graph-theory.tex"
	targetPath := filepath.Join(notesPath, targetFile)
	os.WriteFile(targetPath, []byte("content"), 0644)

	ls := &LanguageServer{
		vault: &vault.Vault{
			NotesPath: notesPath,
		},
		index: NewIndex(),
	}

	ls.index.Set("graph-theory", &NoteHeader{
		Title:    "Graph Theory",
		Slug:     "graph-theory",
		Filename: targetFile,
	})

	// Create test file with reference
	testFile := filepath.Join(notesPath, "test.tex")
	testContent := `\ref{graph-theory}`
	os.WriteFile(testFile, []byte(testContent), 0644)

	params := &protocol.DefinitionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{
				URI: protocol.DocumentURI("file://" + testFile),
			},
			Position: protocol.Position{
				Line:      0,
				Character: 10, // Inside "graph-theory"
			},
		},
	}

	locations, err := ls.Definition(context.Background(), params)
	if err != nil {
		t.Fatalf("Definition failed: %v", err)
	}

	if len(locations) != 1 {
		t.Fatalf("expected 1 location, got %d", len(locations))
	}

	expectedURI := protocol.DocumentURI("file://" + targetPath)
	if locations[0].URI != expectedURI {
		t.Errorf("expected URI %s, got %s", expectedURI, locations[0].URI)
	}
}

// TestHover tests hover information
func TestHover(t *testing.T) {
	ls := &LanguageServer{
		index: NewIndex(),
	}

	ls.index.Set("graph-theory", &NoteHeader{
		Title: "Intro to Graphs",
		Slug:  "graph-theory",
		Date:  "2024-01-01",
		Tags:  []string{"math"},
	})

	tempDir := t.TempDir()
	notesPath := filepath.Join(tempDir, "notes")
	os.MkdirAll(notesPath, 0755)

	testFile := filepath.Join(notesPath, "test.tex")
	testContent := `\ref{graph-theory}`
	os.WriteFile(testFile, []byte(testContent), 0644)

	ls.vault = &vault.Vault{NotesPath: notesPath}

	params := &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{
				URI: protocol.DocumentURI("file://" + testFile),
			},
			Position: protocol.Position{
				Line:      0,
				Character: 10,
			},
		},
	}

	hover, err := ls.Hover(context.Background(), params)
	if err != nil {
		t.Fatalf("Hover failed: %v", err)
	}

	if hover == nil {
		t.Fatal("expected hover result, got nil")
	}

	content := hover.Contents.Value
	if !strings.Contains(content, "Intro to Graphs") {
		t.Errorf("expected title in hover, got: %s", content)
	}
	if !strings.Contains(content, "graph-theory") {
		t.Errorf("expected slug in hover, got: %s", content)
	}
	if !strings.Contains(content, "math") {
		t.Errorf("expected tags in hover, got: %s", content)
	}
}
