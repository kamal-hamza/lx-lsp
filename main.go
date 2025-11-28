// main.go
package main

import (
	"context"
	"os"

	"github.com/kamal-hamza/lx-lsp/server"
)

func main() {
	ctx := context.Background()

	// Create and run the language server
	srv, err := server.NewLanguageServer()
	if err != nil {
		os.Exit(1)
	}

	if err := srv.Run(ctx); err != nil {
		os.Exit(1)
	}
}
