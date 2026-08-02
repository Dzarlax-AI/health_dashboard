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

	if err := rejectProtectedBaseline(*outPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

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

func rejectProtectedBaseline(outPath string) error {
	protectedPath, err := filepath.Abs(filepath.Join("contracts", "openapi.compat.json"))
	if err != nil {
		return fmt.Errorf("resolve protected compatibility baseline: %w", err)
	}
	requestedPath, err := filepath.Abs(filepath.Clean(outPath))
	if err != nil {
		return fmt.Errorf("resolve output path: %w", err)
	}
	if requestedPath == protectedPath {
		return fmt.Errorf("refusing to overwrite protected compatibility baseline %s", outPath)
	}
	return nil
}
