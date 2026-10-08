package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestRunCountryCodeLookupRejectsUnexpectedArgumentsWithoutNetwork(t *testing.T) {
	for _, arguments := range [][]string{nil, {"lookup", "extra"}, {"other"}} {
		if err := runCountryCodeLookup(arguments, io.Discard); err == nil {
			t.Fatalf("accepted arguments %q", arguments)
		}
	}
}

func TestRemoteRemovalOptInDefaultsOffAndRequiresExplicitBoolean(t *testing.T) {
	if enabled, err := parseRemoteRemovalOptIn("", false); err != nil || enabled {
		t.Fatalf("absent opt-in enabled=%t err=%v", enabled, err)
	}
	if enabled, err := parseRemoteRemovalOptIn("false", true); err != nil || enabled {
		t.Fatalf("false opt-in enabled=%t err=%v", enabled, err)
	}
	if enabled, err := parseRemoteRemovalOptIn("true", true); err != nil || !enabled {
		t.Fatalf("true opt-in enabled=%t err=%v", enabled, err)
	}
	if enabled, err := parseRemoteRemovalOptIn("yes", true); err == nil || enabled {
		t.Fatalf("invalid opt-in enabled=%t err=%v", enabled, err)
	}
}

func TestRunSelectorOrderAcceptsInstalledHelperArgumentOrder(t *testing.T) {
	directory := t.TempDir()
	config := filepath.Join(directory, "config.json")
	output := filepath.Join(directory, "selector-order.json")
	if err := os.WriteFile(config, []byte(`{"outbounds":[{"type":"selector","tag":"second"},{"type":"selector","tag":"first"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runSelectorOrder([]string{"extract", "--config", config, "--output", output}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "{\"selectors\":[\"second\",\"first\"]}\n" {
		t.Fatalf("metadata=%s", body)
	}
}

func TestRunSelectorOrderRejectsMissingOrTrailingArguments(t *testing.T) {
	for _, arguments := range [][]string{nil, {"extract"}, {"wrong", "--config", "x", "--output", "y"}, {"extract", "--config", "x", "--output", "y", "trailing"}} {
		if err := runSelectorOrder(arguments); err == nil {
			t.Fatalf("accepted arguments %q", arguments)
		}
	}
}
