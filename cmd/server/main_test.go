package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"404-probe/internal/auth"
)

func TestLoadControlTokenHash(t *testing.T) {
	token, wantHash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "\n", "\r\n"} {
		t.Run(strings.ReplaceAll(suffix, "\n", "LF"), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "control.token")
			if err := os.WriteFile(path, []byte(token+suffix), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := loadControlTokenHash(path)
			if err != nil || !bytes.Equal(got, wantHash) {
				t.Fatalf("hash=%x err=%v", got, err)
			}
		})
	}
}

func TestLoadControlTokenHashRejectsInvalidFiles(t *testing.T) {
	token, _, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		content string
	}{
		{name: "empty"},
		{name: "leading whitespace", content: " " + token},
		{name: "trailing whitespace", content: token + " "},
		{name: "multiple lines", content: token + "\n\n"},
		{name: "embedded newline", content: token[:20] + "\n" + token[20:]},
		{name: "padded base64", content: token + "="},
		{name: "invalid base64", content: strings.Repeat("!", 43)},
		{name: "wrong decoded length", content: strings.Repeat("a", 42)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "control.token")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadControlTokenHash(path); err == nil || strings.Contains(err.Error(), token) {
				t.Fatalf("error=%v", err)
			}
		})
	}

	large := filepath.Join(t.TempDir(), "large.token")
	if err := os.WriteFile(large, bytes.Repeat([]byte("a"), 1025), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadControlTokenHash(large); err == nil {
		t.Fatal("oversized token file accepted")
	}
	if _, err := loadControlTokenHash(t.TempDir()); err == nil {
		t.Fatal("directory accepted as token file")
	}
}

func TestLoadControlTokenHashRejectsBroadUnixPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}
	token, _, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "control.token")
	if err := os.WriteFile(path, []byte(token), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadControlTokenHash(path); err == nil {
		t.Fatal("broad token file permissions accepted")
	}
}
