package releasemetadata

import (
	"strings"
	"testing"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

func validDocument() Document {
	return Document{SchemaVersion: 1, Version: "v0.8.1", Commit: testCommit, Assets: []Asset{
		{Name: "404-probe-agent-linux-amd64", GOOS: "linux", GOARCH: "amd64", SHA256: strings.Repeat("a", 64)},
	}}
}

func TestMetadataRoundTripAndSelection(t *testing.T) {
	data, err := Encode(validDocument())
	if err != nil {
		t.Fatal(err)
	}
	document, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := Select(document, "v0.8.1", "404-probe-agent-linux-amd64", "linux", "amd64")
	if err != nil || asset.SHA256 != strings.Repeat("a", 64) {
		t.Fatalf("asset=%+v err=%v", asset, err)
	}
}

func TestMetadataRejectsInvalidDocuments(t *testing.T) {
	tests := map[string]func(*Document){
		"schema":       func(d *Document) { d.SchemaVersion = 2 },
		"version":      func(d *Document) { d.Version = "0.8.1" },
		"commit":       func(d *Document) { d.Commit = "deadbeef" },
		"assets":       func(d *Document) { d.Assets = nil },
		"duplicate":    func(d *Document) { d.Assets = append(d.Assets, d.Assets[0]) },
		"asset name":   func(d *Document) { d.Assets[0].Name = "../agent" },
		"asset goos":   func(d *Document) { d.Assets[0].GOOS = "windows" },
		"asset goarch": func(d *Document) { d.Assets[0].GOARCH = "arm64" },
		"sha length":   func(d *Document) { d.Assets[0].SHA256 = "aa" },
		"sha case":     func(d *Document) { d.Assets[0].SHA256 = strings.Repeat("A", 64) },
		"sha encoding": func(d *Document) { d.Assets[0].SHA256 = strings.Repeat("z", 64) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			document := validDocument()
			mutate(&document)
			if err := Validate(document); err == nil {
				t.Fatal("invalid metadata accepted")
			}
		})
	}
}

func TestMetadataStrictJSONAndTargetMismatch(t *testing.T) {
	data, err := Encode(validDocument())
	if err != nil {
		t.Fatal(err)
	}
	unknown := strings.Replace(string(data), `"version":`, `"unknown":true,"version":`, 1)
	for _, value := range []string{unknown, string(data) + `{}`} {
		if _, err := Decode([]byte(value)); err == nil {
			t.Fatal("non-strict metadata accepted")
		}
	}
	document := validDocument()
	if _, err := Select(document, "v0.8.2", document.Assets[0].Name, "linux", "amd64"); err == nil {
		t.Fatal("wrong target version accepted")
	}
}
