package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	clientapi "health-receiver/internal/api"
)

func main() {
	outPath := flag.String("out", "contracts/openapi.json", "output path")
	flag.Parse()

	document, err := clientapi.GenerateOpenAPI()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := clientapi.ValidateOpenAPI(document); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*outPath, document, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
