package main

import (
	"path/filepath"
	"testing"
)

func TestRejectProtectedBaseline(t *testing.T) {
	for _, path := range []string{
		"contracts/openapi.compat.json",
		filepath.Join(".", "contracts", "openapi.compat.json"),
	} {
		if err := rejectProtectedBaseline(path); err == nil {
			t.Fatalf("rejectProtectedBaseline(%q) returned nil", path)
		}
	}
	if err := rejectProtectedBaseline("contracts/openapi.json"); err != nil {
		t.Fatalf("generated contract path rejected: %v", err)
	}
}
