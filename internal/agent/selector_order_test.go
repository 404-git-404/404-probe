package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"404-probe/internal/protocol"
)

func TestOrderSelectorsUsesSecretFreeMetadataAndDeterministicFallback(t *testing.T) {
	selectors := func() []protocol.OutboundSelector {
		return []protocol.OutboundSelector{{Name: "Google"}, {Name: "Spotify"}, {Name: "Apple"}}
	}
	path := filepath.Join(t.TempDir(), "selector-order.json")
	if err := os.WriteFile(path, []byte(`{"selectors":["Spotify","Apple","Google"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ordered := selectors()
	if source := orderSelectors(ordered, path); source != "config" {
		t.Fatalf("source=%q", source)
	}
	for index, want := range []string{"Spotify", "Apple", "Google"} {
		if ordered[index].Name != want {
			t.Fatalf("ordered=%v", ordered)
		}
	}

	fallback := selectors()
	if source := orderSelectors(fallback, filepath.Join(t.TempDir(), "missing")); source != "name" {
		t.Fatalf("fallback source=%q", source)
	}
	for index, want := range []string{"Apple", "Google", "Spotify"} {
		if fallback[index].Name != want {
			t.Fatalf("fallback=%v", fallback)
		}
	}

	stale := append(selectors(), protocol.OutboundSelector{Name: "Unknown"})
	if source := orderSelectors(stale, path); source != "name" {
		t.Fatalf("stale metadata source=%q", source)
	}
	for index, want := range []string{"Apple", "Google", "Spotify", "Unknown"} {
		if stale[index].Name != want {
			t.Fatalf("stale fallback=%v", stale)
		}
	}
}

func TestExtractSelectorOrderWritesOnlyOrderedSelectorTags(t *testing.T) {
	directory := t.TempDir()
	config := filepath.Join(directory, "config.json")
	output := filepath.Join(directory, "order.json")
	content := `{"outbounds":[{"type":"direct","tag":"direct"},{"type":"selector","tag":"Spotify","outbounds":["secret-node"]},{"type":"selector","tag":"Apple","password":"must-not-leak"}]}`
	if err := os.WriteFile(config, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ExtractSelectorOrder(config, output); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "{\"selectors\":[\"Spotify\",\"Apple\"]}\n" {
		t.Fatalf("metadata=%s", body)
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil || len(document) != 1 {
		t.Fatalf("document=%v err=%v", document, err)
	}
}

func TestSelectorOrderMetadataFailsClosed(t *testing.T) {
	for _, content := range []string{
		`{"selectors":[]}`,
		`{"selectors":["a","a"]}`,
		`{"selectors":[" a"]}`,
		`{"selectors":["a"],"secret":"must not be accepted"}`,
		`{"selectors":["a"]} trailing`,
	} {
		path := filepath.Join(t.TempDir(), "order.json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := readSelectorOrder(path); ok {
			t.Fatalf("accepted %q", content)
		}
	}
}
