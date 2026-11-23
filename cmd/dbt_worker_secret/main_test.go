package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecretIsPrivateAndCannotReplaceExistingFile(t *testing.T) {
	out := filepath.Join(t.TempDir(), "dsn")
	if err := writeSecret(out, "first"); err != nil {
		t.Fatal(err)
	}
	if err := writeSecret(out, "second"); err == nil {
		t.Fatal("replaced an existing secret")
	}
	b, err := os.ReadFile(out)
	if err != nil || string(b) != "first\n" {
		t.Fatal("original contents lost")
	}
	info, err := os.Stat(out)
	if err != nil || info.Mode().Perm() != 0400 {
		t.Fatal("secret permissions changed")
	}
}
