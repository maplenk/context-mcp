package main

import (
	"fmt"
	"log"
	"os"

	"github.com/naman/qb-context/internal/config"
)

func main() {
	// Route all internal logging to stderr to avoid corrupting MCP stdio JSON-RPC
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	cfg := config.ParseFlags()

	log.Printf("qb-context daemon starting")
	log.Printf("Repository root: %s", cfg.RepoRoot)
	log.Printf("Database path: %s", cfg.DBPath)

	fmt.Fprintln(os.Stderr, "qb-context daemon initialized (scaffold only)")
}
