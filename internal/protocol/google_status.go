package protocol

import (
	"errors"
)

type GoogleStatusConfig struct{}

type YouTubeStatus string

const (
	YouTubeUnknown YouTubeStatus = "unknown"
	YouTubeCN      YouTubeStatus = "cn"
	YouTubeNotCN   YouTubeStatus = "not_cn"
)

type GoogleSearchStatus string

const (
	GoogleSearchUnknown   GoogleSearchStatus = "unknown"
	GoogleSearchOK        GoogleSearchStatus = "ok"
	GoogleSearchChallenge GoogleSearchStatus = "challenge"
	GoogleSearchBlocked   GoogleSearchStatus = "blocked"
)

type GoogleSignInStatus string

const (
	GoogleSignInUnknown   GoogleSignInStatus = "unknown"
	GoogleSignInReachable GoogleSignInStatus = "reachable"
	GoogleSignInChallenge GoogleSignInStatus = "challenge"
	GoogleSignInBlocked   GoogleSignInStatus = "blocked"
)

type GeminiStatus string

const (
	GeminiUnknown   GeminiStatus = "unknown"
	GeminiAvailable GeminiStatus = "available"
	GeminiBlocked   GeminiStatus = "blocked"
)

type GoogleServiceError struct {
	Category string `json:"category,omitempty"`
}

type YouTubeResult struct {
	Status      YouTubeStatus      `json:"status"`
	Region      string             `json:"region,omitempty"`
	SentToChina *bool              `json:"sent_to_china,omitempty"`
	Error       GoogleServiceError `json:"error,omitempty"`
}

type GoogleSearchResult struct {
	Status GoogleSearchStatus `json:"status"`
	Error  GoogleServiceError `json:"error,omitempty"`
}

type GoogleSignInResult struct {
	Status GoogleSignInStatus `json:"status"`
	Error  GoogleServiceError `json:"error,omitempty"`
}

type GeminiResult struct {
	Status GeminiStatus       `json:"status"`
	Region string             `json:"region,omitempty"`
	Error  GoogleServiceError `json:"error,omitempty"`
}

// GoogleStatusResult contains only derived classifications. Response bodies,
// cookies, redirect URLs, and headers never cross the Agent protocol boundary.
type GoogleStatusResult struct {
	YouTube YouTubeResult      `json:"youtube"`
	Search  GoogleSearchResult `json:"search"`
	SignIn  GoogleSignInResult `json:"signin"`
	Gemini  GeminiResult       `json:"gemini"`
}

func (r GoogleStatusResult) Validate() error {
	if !oneOf(string(r.YouTube.Status), "unknown", "cn", "not_cn") ||
		!oneOf(string(r.Search.Status), "unknown", "ok", "challenge", "blocked") ||
		!oneOf(string(r.SignIn.Status), "unknown", "reachable", "challenge", "blocked") ||
		!oneOf(string(r.Gemini.Status), "unknown", "available", "blocked") {
		return errors.New("Google status classification is invalid")
	}
	if (r.YouTube.Status == YouTubeNotCN && (!validUpperRegion(r.YouTube.Region, 2) || r.YouTube.Region == "CN")) ||
		(r.YouTube.Status == YouTubeCN && r.YouTube.Region != "CN") ||
		(r.YouTube.Status == YouTubeUnknown && r.YouTube.Region != "") {
		return errors.New("YouTube region is inconsistent with status")
	}
	if r.YouTube.Status == YouTubeUnknown {
		if r.YouTube.SentToChina != nil {
			return errors.New("unknown YouTube status must not imply sent_to_china")
		}
	} else if r.YouTube.SentToChina == nil || *r.YouTube.SentToChina != (r.YouTube.Status == YouTubeCN) {
		return errors.New("YouTube sent_to_china is inconsistent with status")
	}
	if r.Gemini.Region != "" && (r.Gemini.Status != GeminiAvailable || !validUpperRegion(r.Gemini.Region, 3)) {
		return errors.New("Gemini region is inconsistent with status")
	}
	for _, category := range []string{r.YouTube.Error.Category, r.Search.Error.Category, r.SignIn.Error.Category, r.Gemini.Error.Category} {
		if !oneOf(category, "", "timeout", "network_error", "response_read_error", "response_body_too_large", "redirect_limit", "unexpected_redirect", "invalid_target", "unrecognized_response", "canceled") {
			return errors.New("Google service error category is invalid")
		}
	}
	return nil
}

func (r GoogleStatusResult) HasUnknown() bool {
	return r.YouTube.Status == YouTubeUnknown || r.Search.Status == GoogleSearchUnknown ||
		r.SignIn.Status == GoogleSignInUnknown || r.Gemini.Status == GeminiUnknown
}

// HasRetainedUnknown is the current YouTube-only Server policy. HasUnknown retains
// its legacy four-service meaning for old callers and wire compatibility.
func (r GoogleStatusResult) HasRetainedUnknown() bool {
	return r.YouTube.Status == YouTubeUnknown
}

func validUpperRegion(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for _, char := range value {
		if char < 'A' || char > 'Z' {
			return false
		}
	}
	return true
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}
