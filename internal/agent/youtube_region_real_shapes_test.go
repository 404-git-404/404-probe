package agent

import (
	"os"
	"path/filepath"
	"testing"

	"404-probe/internal/protocol"
)

func ytPage(scripts ...string) string {
	s := `<html><head><title>YouTube Premium</title>`
	for _, x := range scripts {
		s += `<script nonce="n">` + x + `</script>`
	}
	return s + `</head><body></body></html>`
}

const ytStub = `var ytcfg={d:function(){return window.yt&&yt.config_||ytcfg.data_||(ytcfg.data_={})},get:function(k,o){return k in ytcfg.d()?ytcfg.d()[k]:o},set:function(){var a=arguments;if(a.length>1)ytcfg.d()[a[0]]=a[1];else{var k;for(k in a[0])ytcfg.d()[k]=a[0][k]}}};
window.ytcfg.set('EMERGENCY_BASE_URL', '\/error_204?t\x3djserror\x26level\x3dERROR');`

func ytBoot(cfg string) string {
	return "(function ytBootstrapConfig() {window.ytplayer={};\nytcfg.set(" + cfg + `); window.ytcfg.obfuscatedData_ = [];var setMessage=function(msg){if(window.yt&&yt.setMsg)yt.setMsg(msg);else{window.ytcfg=window.ytcfg||{};ytcfg.msgs=msg}};
setMessage({"__lang__":"en"});})();ytcfg.set("initialInnerWidth",window.innerWidth);ytcfg.set("initialInnerHeight",window.innerHeight);
//# sourceURL=yt-desktop-config.js`
}

const ytTailKV = `ytcfg.set('initialBodyClientWidth', document.body.clientWidth);//# sourceURL=yt-x.js`
const ytTailObject = `ytcfg.set({"CSI_SERVICE_NAME": 'youtube', "TIMING_INFO": {}})`

func TestYouTubeRegionRealPageShapes(t *testing.T) {
	good := `{"INNERTUBE_CONTEXT":{"client":{"hl":"en","gl":"MY"}},"INNERTUBE_CONTEXT_GL":"MY","INNERTUBE_CONTEXT_HL":"en"}`
	for _, tc := range []struct{ name, body, region, reason string }{
		{"real shape", ytPage(ytStub, ytBoot(good), ytTailKV, ytTailObject), "MY", ""},
		{"real shape CN", ytPage(ytStub, ytBoot(`{"INNERTUBE_CONTEXT_GL":"CN"}`), ytTailKV, ytTailObject), "CN", ""},
		{"kv region", ytPage(ytStub, ytBoot(good), `ytcfg.set('INNERTUBE_CONTEXT_GL','CN')`), "", "invalid_configuration"},
		{"js object region", ytPage(ytBoot(good), `ytcfg.set({"INNERTUBE_CONTEXT_GL": 'CN'})`), "", "invalid_configuration"},
		{"js object gl", ytPage(ytBoot(good), `ytcfg.set({x:{gl:'CN'}})`), "", "invalid_configuration"},
		{"js object contentRegion", ytPage(ytBoot(good), `ytcfg.set({"contentRegion": 'CN'})`), "", "invalid_configuration"},
		{"uninvoked", ytPage(`(function(){ytcfg.set({"INNERTUBE_CONTEXT_GL":"CN"});});`), "", "missing_evidence"},
		{"returned", ytPage(`(function(){return;ytcfg.set({"INNERTUBE_CONTEXT_GL":"CN"});})();`), "", "missing_evidence"},
		{"nested iife", ytPage(`(function(){(function(){ytcfg.set({"INNERTUBE_CONTEXT_GL":"CN"});})();})();`), "", "missing_evidence"},
		{"conditional", ytPage(`(function(){if(x)ytcfg.set({"INNERTUBE_CONTEXT_GL":"CN"});})();`), "", "missing_evidence"},
		{"assigned", ytPage(`var f=(function(){ytcfg.set({"INNERTUBE_CONTEXT_GL":"CN"});})();`), "", "missing_evidence"},
		{"boot vs top conflict", ytPage(ytBoot(good), `ytcfg.set({"INNERTUBE_CONTEXT_GL":"US"})`), "", "region_conflict"},
		{"boot dup key", ytPage(ytBoot(`{"INNERTUBE_CONTEXT_GL":"US","INNERTUBE_CONTEXT_GL":"CN"}`)), "", "invalid_configuration"},
		// A ytcfg script this reader cannot parse but that has nothing to do with the region no longer ruins the page.
		{"division in kv, region elsewhere", ytPage(ytBoot(good), `ytcfg.set("w",window.innerWidth/2)`), "MY", ""},
		{"unrelated unparseable script alone", ytPage(`ytcfg.set("w",window.innerWidth/2)`), "", "missing_evidence"},
		{"unrelated unterminated comment", ytPage(ytBoot(good), `ytcfg.set("a",1); /* never closed`), "MY", ""},
		{"unrelated ambiguous regex", ytPage(ytBoot(good), `ytcfg.set("a",1); x = /abc`), "MY", ""},
		// ... while a region-bearing ytcfg script that cannot be read still invalidates it.
		{"region-bearing division", ytPage(ytBoot(good), `ytcfg.set("INNERTUBE_CONTEXT_GL",window.w/2)`), "", "invalid_configuration"},
		{"region-bearing unterminated comment", ytPage(ytBoot(good), `ytcfg.set({"gl":"CN"}); /* never closed`), "", "invalid_configuration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseYouTube(htmlResponse(200, googleTargets[0].url, tc.body))
			if got.Region != tc.region || got.Error.Category != tc.reason {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestYouTubeRegionSanitizedRealPages(t *testing.T) {
	for _, tc := range []struct {
		file, region string
		status       protocol.YouTubeStatus
	}{
		{"premium-my.html", "MY", protocol.YouTubeNotCN},
		// Real EU responses, reached only by following the consent redirect (which the Agent never does).
		{"premium-de.html", "DE", protocol.YouTubeNotCN},
		{"premium-it-from-nl-host.html", "IT", protocol.YouTubeNotCN},
		{"premium-us-a.html", "US", protocol.YouTubeNotCN},
		// Contains an unrelated www.google.cn ad URL; it must never become CN.
		{"premium-us-b-google-cn-ad.html", "US", protocol.YouTubeNotCN},
	} {
		t.Run(tc.file, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("testdata", "youtube", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			got := parseYouTube(htmlResponse(200, googleTargets[0].url, string(body)))
			if got.Status != tc.status || got.Region != tc.region || got.Error.Category != "" {
				t.Fatalf("%+v", got)
			}
		})
	}
}

// A non-object ytcfg.set call (key/value, single-quoted JS literal, expression)
// that cannot carry a region is skipped; it must not discard the whole page.
func TestYouTubeRegionSkipsNonObjectCalls(t *testing.T) {
	good := `{"INNERTUBE_CONTEXT":{"client":{"hl":"en","gl":"MY"}},"INNERTUBE_CONTEXT_GL":"MY"}`
	for name, extra := range map[string]string{
		"key value":         `ytcfg.set('initialBodyClientWidth', document.body.clientWidth);`,
		"single quoted":     `ytcfg.set({"CSI_SERVICE_NAME": 'youtube', "TIMING_INFO": {}})`,
		"window expression": `ytcfg.set("initialInnerWidth",window.innerWidth);`,
		"emergency url":     `window.ytcfg.set('EMERGENCY_BASE_URL', '\/error_204?t=jserror');`,
	} {
		t.Run(name, func(t *testing.T) {
			got := parseYouTube(htmlResponse(200, googleTargets[0].url, ytPage(ytBoot(good), extra)))
			if got.Region != "MY" || got.Error.Category != "" {
				t.Fatalf("%+v", got)
			}
		})
	}
}
