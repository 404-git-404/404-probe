package web

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

func TestN3EmbeddedFixedLocalQualityDependencies(t *testing.T) {
	assets, err := fs.Sub(Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	for name, digest := range map[string]string{
		"vendor/uplot/uPlot.iife.min.js": "19c8d4c6ad88929a79f4ae49d6f7161566dfd0ba3d15cc495e974f787eb78f1f",
		"vendor/uplot/uPlot.min.css":     "df630c6a8d6f8eeaff264b50f73ce5b114f646ffd9a0bb74f049b0a00135fa04",
		"vendor/uplot/LICENSE":           "8f989229699b4fe2f1a0432d0e9edc338a8a911e250e2d1b01ecd770a5f5b1bd",
	} {
		data, err := fs.ReadFile(assets, name)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != digest {
			t.Fatalf("%s fixed official SHA changed: %s", name, got)
		}
	}
	html, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	previous := -1
	for _, script := range []string{"agent-state.js", "management.js", "vendor/uplot/uPlot.iife.min.js", "network-quality-core.js", "network-quality.js", "app.js"} {
		index := strings.Index(string(html), `<script src="/`+script+`" defer>`)
		if index <= previous {
			t.Fatalf("script dependency order: %s", script)
		}
		previous = index
	}
	if strings.Count(string(html), `id="network-quality-history"`) != 1 {
		t.Fatal("must embed exactly one quality history dialog")
	}
	for _, name := range []string{"network-quality-core.js", "network-quality.js", "network-quality.css"} {
		data, err := fs.ReadFile(assets, name)
		if err != nil {
			t.Fatal(err)
		}
		source := string(data)
		if name == "network-quality.js" {
			// M09 persists only the selected address-family UI preference.
			// Keep rejecting any other storage access, including history data.
			for _, preference := range []string{
				`root.localStorage?.setItem('404-probe:nq-family:'+card.id,value)`,
				`root.localStorage?.getItem('404-probe:nq-family:'+agent.agent_id)`,
			} {
				source = strings.Replace(source, preference, "", 1)
			}
		}
		for _, forbidden := range []string{"https://", "http://", "localStorage", "sessionStorage", "eval(", "new Function("} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("%s runtime dependency/persistence: %s", name, forbidden)
			}
		}
	}
}

// R07: the page reads and writes the v4 API only, builds no markup from text, and carries the texts
// the design requires. The behavior itself is tested by scripts/test-r07-network-quality.cjs.
func TestR07QualityPageSources(t *testing.T) {
	assets, err := fs.Sub(Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		data, err := fs.ReadFile(assets, name)
		if err != nil || len(data) == 0 {
			t.Fatalf("%s: %v", name, err)
		}
		return string(data)
	}
	for _, name := range []string{"network-quality.js", "network-quality-core.js", "latency-mini.js", "device-details.js"} {
		source := read(name)
		for _, forbidden := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "createContextualFragment", "DOMParser"} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("%s builds markup: %s", name, forbidden)
			}
		}
	}
	for _, name := range []string{"network-quality.js", "network-quality-core.js", "latency-mini.js"} {
		source := read(name)
		for _, gone := range []string{"/network-quality/catalog", "card-config", "v2/config", "region_id", "chooseRegion", "V6 检测", "三网共用地区"} {
			if strings.Contains(source, gone) {
				t.Fatalf("%s still has the catalog era: %s", name, gone)
			}
		}
	}
	page := read("network-quality.js")
	for _, need := range []string{"v4/config", "v4/enabled", "请仅检测你有权检测的目标", "放弃修改？", "保存并开启", "开启后会开始检测这些目标", "页面已过期，请刷新", "网络质量设置"} {
		if !strings.Contains(page, need) {
			t.Fatalf("network-quality.js lacks %q", need)
		}
	}
	core := read("network-quality-core.js")
	for _, need := range []string{"需升级 Agent", "本机无 IPv6", "不支持 ICMP", "目标异常", "未配置目标", "未启用", "已关闭", "未测 TCP（只开了 ICMP）", "解析失败", "v5 卡片契约不匹配"} {
		if !strings.Contains(core, need) {
			t.Fatalf("network-quality-core.js lacks %q", need)
		}
	}
	if !strings.Contains(read("network-quality.css"), ".nq-overlay") || !strings.Contains(read("network-quality.css"), ".nq-dialog") {
		t.Fatal("dialog styles missing")
	}
	mini := read("latency-mini.js")
	for _, need := range []string{"tip.setAttribute('role','tooltip')", "head.title='每分钟一个点", "node.append(head,legend,plot,rangeRow,output,tip)"} {
		if !strings.Contains(mini, need) {
			t.Fatalf("R33 curve/hover structure missing %q", need)
		}
	}
	for _, gone := range []string{"lm7-marks", "lm7-caption", "/v5/marks"} {
		if strings.Contains(mini, gone) {
			t.Fatalf("R33 curve retains retired mark strip or bottom caption %q", gone)
		}
	}
	for _, name := range []string{"network-quality.js", "network-quality-core.js", "latency-mini.js"} {
		if strings.Contains(read(name), "tcp-mini") || strings.Contains(read(name), "card-history") {
			t.Fatalf("%s still calls a retired route", name)
		}
	}
}
