package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"404-probe/internal/protocol"
)

const (
	zwiebackCN    = `["https://www.google.com/pagead/lvz?evtid=x","https://www.google.cn/pagead/lvz?evtid=x"]`
	zwiebackClean = `["https://www.google.com/pagead/lvz?evtid=x"]`
)

func chinaConfig(zwieback, rest string) string {
	if rest != "" {
		rest = "," + rest
	}
	return `{"ZWIEBACK_PING_URLS":` + zwieback + rest + `}`
}

func validYouTubeResult(t *testing.T, y protocol.YouTubeResult) {
	t.Helper()
	result := protocol.GoogleStatusResult{YouTube: y, Search: protocol.GoogleSearchResult{Status: protocol.GoogleSearchUnknown},
		SignIn: protocol.GoogleSignInResult{Status: protocol.GoogleSignInUnknown}, Gemini: protocol.GeminiResult{Status: protocol.GeminiUnknown}}
	if err := result.Validate(); err != nil {
		t.Fatalf("%+v: %v", y, err)
	}
}

func TestYouTubeChinaSignalRealShapes(t *testing.T) {
	read := func(name string) string {
		body, err := os.ReadFile(filepath.Join("testdata", "youtube", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	flagged := parseYouTube(htmlResponse(200, googleTargets[0].url, read("premium-cn-flagged-us-gl.html")))
	if flagged.Status != protocol.YouTubeCN || flagged.Region != "US" || flagged.SentToChina == nil || !*flagged.SentToChina ||
		flagged.PremiumNotice == nil || !*flagged.PremiumNotice || flagged.Error.Category != "" {
		t.Fatalf("flagged host: %+v", flagged)
	}
	validYouTubeResult(t, flagged)
	clean := parseYouTube(htmlResponse(200, googleTargets[0].url, read("premium-us-zwieback-clean.html")))
	if clean.Status != protocol.YouTubeNotCN || clean.Region != "US" || clean.PremiumNotice != nil || clean.Error.Category != "" {
		t.Fatalf("clean host: %+v", clean)
	}
	validYouTubeResult(t, clean)
	// The v1.0.3 fixture is synthetic: google.cn outside the parsed configuration is never a verdict, and
	// the reader will not claim the visitor is outside China either.
	synthetic := parseYouTube(htmlResponse(200, googleTargets[0].url, read("premium-synthetic-google-cn-outside-config.html")))
	if synthetic.Status != protocol.YouTubeUnknown || synthetic.Error.Category != "china_signal_unverified" || synthetic.SentToChina != nil {
		t.Fatalf("synthetic: %+v", synthetic)
	}
}

func TestYouTubeChinaSignalRule(t *testing.T) {
	gl := func(code string) string { return `"INNERTUBE_CONTEXT_GL":"` + code + `"` }
	elsewhere := func(body string) string {
		return strings.Replace(body, "</body>", `<a href="https://www.google.cn/">x</a></body>`, 1)
	}
	for _, tc := range []struct {
		name, body, status, region, reason string
	}{
		{"china ping url", youtubeFixturePage(chinaConfig(zwiebackCN, gl("US"))), "cn", "US", ""},
		{"bare google.cn", youtubeFixturePage(chinaConfig(`["https://google.cn/x"]`, gl("US"))), "cn", "US", ""},
		{"http scheme", youtubeFixturePage(chinaConfig(`["http://www.google.cn/x"]`, gl("US"))), "cn", "US", ""},
		{"upper case host", youtubeFixturePage(chinaConfig(`["HTTPS://WWW.GOOGLE.CN/x"]`, gl("US"))), "cn", "US", ""},
		{"trailing dot host", youtubeFixturePage(chinaConfig(`["https://www.google.cn./x"]`, gl("US"))), "cn", "US", ""},
		{"port ignored", youtubeFixturePage(chinaConfig(`["https://www.google.cn:8443/x"]`, gl("US"))), "cn", "US", ""},
		{"china wins over every non-china GL", youtubeFixturePage(chinaConfig(zwiebackCN, gl("HK"))), "cn", "HK", ""},
		{"china with GL missing", youtubeFixturePage(chinaConfig(zwiebackCN, "")), "cn", "", ""},
		{"china with region conflict", youtubeFixturePage(chinaConfig(zwiebackCN, gl("US")+`,"contentRegion":"JP"`)), "cn", "", ""},
		{"china with invalid region stays invalid", youtubeFixturePage(chinaConfig(zwiebackCN, gl("ZZ"))), "unknown", "", "invalid_region"},
		{"GL CN without ping url", youtubeFixturePage(`{` + gl("CN") + `}`), "cn", "CN", ""},
		{"GL CN with clean ping url", youtubeFixturePage(chinaConfig(zwiebackClean, gl("CN"))), "cn", "CN", ""},
		{"two configs agree", strings.Replace(youtubeFixturePage(chinaConfig(zwiebackCN, gl("US"))), "</script>", `;ytcfg.set(`+chinaConfig(zwiebackCN, gl("US"))+`);</script>`, 1), "cn", "US", ""},
		{"two configs disagree", strings.Replace(youtubeFixturePage(chinaConfig(zwiebackCN, gl("US"))), "</script>", `;ytcfg.set(`+chinaConfig(zwiebackClean, gl("US"))+`);</script>`, 1), "unknown", "", "china_signal_unverified"},
		{"clean ping url", youtubeFixturePage(chinaConfig(zwiebackClean, gl("US"))), "not_cn", "US", ""},
		{"empty ping array", youtubeFixturePage(chinaConfig(`[]`, gl("US"))), "not_cn", "US", ""},
		{"lookalike notgoogle.cn", youtubeFixturePage(chinaConfig(`["https://notgoogle.cn/x"]`, gl("US"))), "not_cn", "US", ""},
		{"lookalike google.cn.evil.com", youtubeFixturePage(chinaConfig(`["https://google.cn.evil.com/x"]`, gl("US"))), "unknown", "", "china_signal_unverified"},
		{"userinfo", youtubeFixturePage(chinaConfig(`["https://google.cn@evil.example/x"]`, gl("US"))), "unknown", "", "china_signal_unverified"},
		{"protocol relative", youtubeFixturePage(chinaConfig(`["//google.cn/x"]`, gl("US"))), "unknown", "", "china_signal_unverified"},
		{"javascript url", youtubeFixturePage(chinaConfig(`["javascript:google.cn"]`, gl("US"))), "unknown", "", "china_signal_unverified"},
		{"ping value not an array", youtubeFixturePage(chinaConfig(`"https://www.google.cn/x"`, gl("US"))), "unknown", "", "china_signal_unverified"},
		{"ping element not a string", youtubeFixturePage(chinaConfig(`[1,"https://www.google.cn/x"]`, gl("US"))), "unknown", "", "china_signal_unverified"},
		{"ping key nested, not top level", youtubeFixturePage(`{"INNERTUBE_CONTEXT":{"x":{"ZWIEBACK_PING_URLS":` + zwiebackCN + `}},` + gl("US") + `}`), "unknown", "", "china_signal_unverified"},
		{"google.cn only elsewhere", elsewhere(youtubeFixturePage(`{` + gl("US") + `}`)), "unknown", "", "china_signal_unverified"},
		{"google.cn elsewhere with GL missing", elsewhere(youtubeFixturePage(`{"x":1}`)), "unknown", "", "china_signal_unverified"},
		{"google.cn elsewhere with region conflict", elsewhere(youtubeFixturePage(`{` + gl("US") + `,"contentRegion":"JP"}`)), "unknown", "", "china_signal_unverified"},
		{"google.cn elsewhere does not hide invalid region", elsewhere(youtubeFixturePage(`{` + gl("ZZ") + `}`)), "unknown", "", "invalid_region"},
		{"google.cn elsewhere but china confirmed", elsewhere(youtubeFixturePage(chinaConfig(zwiebackCN, gl("US")))), "cn", "US", ""},
		{"consent page with google.cn", strings.Replace(youtubeFixturePage(chinaConfig(zwiebackCN, gl("US"))), "YouTube Premium", "Before you continue to YouTube", 1), "unknown", "", "consent_page"},
		{"challenge page with google.cn", strings.Replace(youtubeFixturePage(chinaConfig(zwiebackCN, gl("US"))), "</body>", `<div class="g-recaptcha"></div></body>`, 1), "unknown", "", "challenge_page"},
		{"unparseable region config", youtubeFixturePage(`{` + gl("US") + `,"ZWIEBACK_PING_URLS":` + zwiebackCN + `,"a":1,"a":2}`), "unknown", "", "invalid_configuration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseYouTube(htmlResponse(200, googleTargets[0].url, tc.body))
			if string(got.Status) != tc.status || got.Region != tc.region || got.Error.Category != tc.reason {
				t.Fatalf("%+v", got)
			}
			if tc.status == "cn" && (got.SentToChina == nil || !*got.SentToChina || got.PremiumNotice == nil) {
				t.Fatalf("cn result lacks its markers: %+v", got)
			}
			if tc.status != "cn" && got.PremiumNotice != nil {
				t.Fatalf("premium notice outside a cn result: %+v", got)
			}
			validYouTubeResult(t, got)
		})
	}
}

func TestYouTubeChinaSignalPremiumNotice(t *testing.T) {
	page := func(config, extraBody string) string {
		return strings.Replace(youtubeFixturePage(config), "</body>", extraBody+"</body>", 1)
	}
	const sentence = `<script>var ytInitialData={"title":{"content":"YouTube Premium is not available in your country"}};</script>`
	with := parseYouTube(htmlResponse(200, googleTargets[0].url, page(chinaConfig(zwiebackCN, `"INNERTUBE_CONTEXT_GL":"US"`), sentence)))
	without := parseYouTube(htmlResponse(200, googleTargets[0].url, page(chinaConfig(zwiebackCN, `"INNERTUBE_CONTEXT_GL":"US"`), "")))
	if with.PremiumNotice == nil || !*with.PremiumNotice || without.PremiumNotice == nil || *without.PremiumNotice {
		t.Fatalf("with=%+v without=%+v", with, without)
	}
	// The sentence never produces a China verdict on its own.
	alone := parseYouTube(htmlResponse(200, googleTargets[0].url, page(`{"INNERTUBE_CONTEXT_GL":"US"}`, sentence)))
	if alone.Status != protocol.YouTubeNotCN || alone.PremiumNotice != nil {
		t.Fatalf("%+v", alone)
	}
}

func TestGoogleStatusOlderServerShape(t *testing.T) {
	yes, no := true, false
	china := protocol.GoogleStatusResult{YouTube: protocol.YouTubeResult{Status: protocol.YouTubeCN, Region: "US", SentToChina: &yes, PremiumNotice: &yes}}
	googleStatusOlderServerShape(&china)
	if china.YouTube.Region != "CN" || china.YouTube.PremiumNotice != nil || china.YouTube.Status != protocol.YouTubeCN {
		t.Fatalf("%+v", china.YouTube)
	}
	validYouTubeResult(t, china.YouTube)
	empty := protocol.GoogleStatusResult{YouTube: protocol.YouTubeResult{Status: protocol.YouTubeCN, SentToChina: &yes, PremiumNotice: &no}}
	googleStatusOlderServerShape(&empty)
	if empty.YouTube.Region != "CN" || empty.YouTube.PremiumNotice != nil {
		t.Fatalf("%+v", empty.YouTube)
	}
	normal := protocol.GoogleStatusResult{YouTube: protocol.YouTubeResult{Status: protocol.YouTubeNotCN, Region: "US", SentToChina: &no}}
	googleStatusOlderServerShape(&normal)
	if normal.YouTube.Region != "US" || normal.YouTube.Status != protocol.YouTubeNotCN {
		t.Fatalf("%+v", normal.YouTube)
	}
	googleStatusOlderServerShape(nil)
}

func TestYouTubeChinaSignalUnverifiedUsesCompatibleCategory(t *testing.T) {
	executor := &GoogleStatusExecutor{fetcher: fixtureGoogleFetcher{googleYouTube: {htmlResponse(200, "https://www.youtube.com/premium",
		strings.Replace(youtubeFixturePage(`{"INNERTUBE_CONTEXT_GL":"US"}`), "</body>", `<a href="https://www.google.cn/">x</a></body>`, 1)), ""}}}
	job := protocol.Job{ProbeType: protocol.ProbeTypeGoogleStatus, Config: protocol.ProbeConfig{GoogleStatus: &protocol.GoogleStatusConfig{}}}
	execution, err := executor.Execute(t.Context(), job)
	if err != nil || execution.Success || execution.ErrorCategory != "youtube_china_signal_unverified" ||
		execution.Result.GoogleStatus.YouTube.Error.Category != "unrecognized_response" || execution.Result.GoogleStatus.Validate() != nil {
		t.Fatalf("%+v err=%v", execution, err)
	}
}

// A Server that did not advertise the capability only gets the older cn shape, whatever the page said.
func TestRunnerReportsOlderCNShapeUntilServerAdvertisesChinaSignal(t *testing.T) {
	body := youtubeFixturePage(chinaConfig(zwiebackCN, `"INNERTUBE_CONTEXT_GL":"US"`))
	job := protocol.Job{JobID: "job", ProbeType: protocol.ProbeTypeGoogleStatus, TimeoutMS: 5000, Attempt: 1, LeaseToken: "lease",
		Config: protocol.ProbeConfig{GoogleStatus: &protocol.GoogleStatusConfig{}}}
	for _, advertised := range []bool{false, true} {
		executor := &GoogleStatusExecutor{fetcher: fixtureGoogleFetcher{googleYouTube: {htmlResponse(200, "https://www.youtube.com/premium", body), ""}}}
		runner := &Runner{epoch: 1, sessionID: "session", executor: executor}
		runner.googleChinaSignalSupported.Store(advertised)
		result := runner.executeJob(context.Background(), job)
		if !result.Success || result.Result.GoogleStatus == nil {
			t.Fatalf("advertised=%t %+v", advertised, result)
		}
		got := result.Result.GoogleStatus.YouTube
		if got.Status != protocol.YouTubeCN || got.SentToChina == nil || !*got.SentToChina {
			t.Fatalf("advertised=%t %+v", advertised, got)
		}
		if advertised && (got.Region != "US" || got.PremiumNotice == nil) {
			t.Fatalf("full shape lost: %+v", got)
		}
		if !advertised && (got.Region != "CN" || got.PremiumNotice != nil) {
			t.Fatalf("older shape not used: %+v", got)
		}
	}
}
