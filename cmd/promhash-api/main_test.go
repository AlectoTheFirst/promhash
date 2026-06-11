package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadTokensEnvOnly(t *testing.T) {
	got, err := loadTokens("", "tok-a, tok-b ,,tok-a")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tok-a", "tok-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestLoadTokensFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokens")
	content := "# ops team\ntok-file-1\n\ntok-file-2  # trailing comment\n"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadTokens(p, "tok-env")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tok-file-1", "tok-file-2", "tok-env"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestLoadTokensMissingFile(t *testing.T) {
	if _, err := loadTokens("/nonexistent/tokens", ""); err == nil {
		t.Fatal("expected error for missing token file")
	}
}

func TestLoadTokensEmpty(t *testing.T) {
	got, err := loadTokens("", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no tokens, got %v", got)
	}
}
