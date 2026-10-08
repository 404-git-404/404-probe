package protocol

import (
	"encoding/json"
	"testing"
)

func TestGoogleStatusRetainedPolicyAndLegacyWireCompatibility(t *testing.T) {
	sent := false
	result := GoogleStatusResult{YouTube: YouTubeResult{Status: YouTubeNotCN, Region: "JP", SentToChina: &sent},
		Gemini: GeminiResult{Status: GeminiAvailable, Region: "JPN"},
		Search: GoogleSearchResult{Status: GoogleSearchUnknown}, SignIn: GoogleSignInResult{Status: GoogleSignInUnknown}}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	if result.HasRetainedUnknown() || !result.HasUnknown() {
		t.Fatalf("new/legacy policies conflated: %+v", result)
	}
	for _, legacy := range []bool{false, true} {
		if legacy {
			result.Search.Status = GoogleSearchOK
			result.SignIn.Status = GoogleSignInReachable
		}
		encoded, err := json.Marshal(ProbeResult{GoogleStatus: &result})
		if err != nil {
			t.Fatal(err)
		}
		var restored ProbeResult
		if err := json.Unmarshal(encoded, &restored); err != nil {
			t.Fatal(err)
		}
		if restored.GoogleStatus == nil || restored.GoogleStatus.Validate() != nil || restored.GoogleStatus.Search.Status != result.Search.Status || restored.GoogleStatus.SignIn.Status != result.SignIn.Status || restored.GoogleStatus.Gemini != result.Gemini {
			t.Fatalf("wire=%s result=%+v", encoded, restored)
		}
	}
	result.YouTube = YouTubeResult{Status: YouTubeUnknown}
	if !result.HasRetainedUnknown() {
		t.Fatal("retained YouTube unknown ignored")
	}
	result.YouTube = YouTubeResult{Status: YouTubeNotCN, Region: "JP", SentToChina: &sent}
	result.Gemini = GeminiResult{Status: GeminiUnknown}
	if result.HasRetainedUnknown() || !result.HasUnknown() {
		t.Fatal("removed Gemini unknown changed current policy or legacy semantics")
	}
	encoded, err := json.Marshal(ProbeResult{GoogleStatus: &result})
	if err != nil {
		t.Fatal(err)
	}
	var restored ProbeResult
	if err := json.Unmarshal(encoded, &restored); err != nil || restored.GoogleStatus == nil || restored.GoogleStatus.Validate() != nil || restored.GoogleStatus.Gemini != result.Gemini {
		t.Fatalf("retired Gemini wire=%s result=%+v err=%v", encoded, restored, err)
	}
	result.Search.Status = ""
	if result.Validate() == nil {
		t.Fatal("invalid legacy slot silently accepted")
	}
}
