package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func youtubeOnly(y YouTubeResult) GoogleStatusResult {
	return GoogleStatusResult{YouTube: y, Search: GoogleSearchResult{Status: GoogleSearchUnknown}, SignIn: GoogleSignInResult{Status: GoogleSignInUnknown}, Gemini: GeminiResult{Status: GeminiUnknown}}
}

func TestYouTubeChinaResultKeepsPageRegion(t *testing.T) {
	yes, no := true, false
	for name, y := range map[string]YouTubeResult{
		"page region and notice": {Status: YouTubeCN, Region: "US", SentToChina: &yes, PremiumNotice: &yes},
		"no notice observed":     {Status: YouTubeCN, Region: "US", SentToChina: &yes, PremiumNotice: &no},
		"no page region":         {Status: YouTubeCN, SentToChina: &yes, PremiumNotice: &yes},
		"older shape":            {Status: YouTubeCN, Region: "CN", SentToChina: &yes},
		"normal region":          {Status: YouTubeNotCN, Region: "US", SentToChina: &no},
	} {
		if err := youtubeOnly(y).Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, y := range map[string]YouTubeResult{
		"cn with a lower-case region":     {Status: YouTubeCN, Region: "us", SentToChina: &yes},
		"cn with a three-letter region":   {Status: YouTubeCN, Region: "USA", SentToChina: &yes},
		"cn without sent_to_china":        {Status: YouTubeCN, Region: "US"},
		"cn with sent_to_china false":     {Status: YouTubeCN, Region: "US", SentToChina: &no},
		"not_cn must not carry CN":        {Status: YouTubeNotCN, Region: "CN", SentToChina: &no},
		"not_cn must not carry a notice":  {Status: YouTubeNotCN, Region: "US", SentToChina: &no, PremiumNotice: &yes},
		"unknown must not carry a notice": {Status: YouTubeUnknown, PremiumNotice: &yes},
		"unknown must not carry a region": {Status: YouTubeUnknown, Region: "US"},
	} {
		if err := youtubeOnly(y).Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestChinaSignalUnverifiedCategoryAndOlderServerMapping(t *testing.T) {
	if !YouTubeDiagnosticCategory("china_signal_unverified") {
		t.Fatal("new category is not an allowed diagnostic")
	}
	// An older Server only knows the legacy categories, so the wire result carries a compatible one.
	if got := CompatibleYouTubeCategory("china_signal_unverified"); got != "unrecognized_response" {
		t.Fatalf("compatible category=%q", got)
	}
	result := youtubeOnly(YouTubeResult{Status: YouTubeUnknown, Error: GoogleServiceError{Category: CompatibleYouTubeCategory("china_signal_unverified")}})
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPremiumNoticeWireShapeAndCapability(t *testing.T) {
	yes := true
	encoded, err := json.Marshal(youtubeOnly(YouTubeResult{Status: YouTubeCN, Region: "US", SentToChina: &yes, PremiumNotice: &yes}))
	if err != nil || !strings.Contains(string(encoded), `"premium_unavailable_notice":true`) {
		t.Fatalf("%s %v", encoded, err)
	}
	var restored GoogleStatusResult
	if err := json.Unmarshal(encoded, &restored); err != nil || restored.Validate() != nil || restored.YouTube.PremiumNotice == nil || !*restored.YouTube.PremiumNotice {
		t.Fatalf("%+v %v", restored, err)
	}
	older, err := json.Marshal(youtubeOnly(YouTubeResult{Status: YouTubeCN, Region: "CN", SentToChina: &yes}))
	if err != nil || strings.Contains(string(older), "premium_unavailable_notice") {
		t.Fatalf("the older shape must not mention the new field: %s %v", older, err)
	}
	capabilities, err := json.Marshal(ReportCapabilities{GoogleStatusChinaSignal: true})
	if err != nil || string(capabilities) != `{"google_status_china_signal":true}` {
		t.Fatalf("%s %v", capabilities, err)
	}
}
