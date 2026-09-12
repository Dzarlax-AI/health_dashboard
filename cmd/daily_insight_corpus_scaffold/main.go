// daily_insight_corpus_scaffold creates a reviewable draft B1 corpus from an
// already sanitized candidate export. It is local-only: it makes no database
// connection, provider request, bundle write or feature-flag change.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"health-receiver/internal/ai"
)

func main() {
	candidatesPath := flag.String("candidates", "", "sanitized candidate export JSON")
	out := flag.String("out", "", "draft corpus JSON outside the repository")
	flag.Parse()
	if *candidatesPath == "" || *out == "" {
		log.Fatal("--candidates and --out are required")
	}
	raw, err := os.ReadFile(*candidatesPath)
	if err != nil {
		log.Fatalf("read candidate export: %v", err)
	}
	var export ai.DailyInsightNarrativeCandidateExport
	if err := json.Unmarshal(raw, &export); err != nil {
		log.Fatalf("decode candidate export: %v", err)
	}
	corpus, err := ai.BuildDailyInsightNarrativeCorpusScaffold(export)
	if err != nil {
		log.Fatalf("build draft corpus: %v", err)
	}
	encoded, err := json.MarshalIndent(corpus, "", "  ")
	if err != nil {
		log.Fatalf("encode draft corpus: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o700); err != nil {
		log.Fatalf("create output directory: %v", err)
	}
	if err := os.WriteFile(*out, append(encoded, '\n'), 0o600); err != nil {
		log.Fatalf("write draft corpus: %v", err)
	}
	hash, err := ai.DailyInsightNarrativeCorpusHash(corpus)
	if err != nil {
		log.Fatalf("hash draft corpus: %v", err)
	}
	fmt.Printf("wrote %d-case draft corpus (%s); review and freeze before evaluation\n", len(corpus.Cases), hash)
}
