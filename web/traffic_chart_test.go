package web

import (
	"io/fs"
	"strings"
	"testing"
)

func TestTrafficChartEmbeddedSharedMonitorContract(t *testing.T) {
	assets, err := fs.Sub(Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	html, _ := fs.ReadFile(assets, "index.html")
	previous := -1
	for _, name := range []string{"vendor/uplot/uPlot.iife.min.js", "network-quality-core.js", "network-quality.js", "traffic-chart.js", "app.js"} {
		index := strings.Index(string(html), `<script src="/`+name+`" defer>`)
		if index <= previous {
			t.Fatalf("script order: %s", name)
		}
		previous = index
	}
	if !strings.Contains(string(html), `href="/traffic-chart.css"`) {
		t.Fatal("traffic CSS not embedded")
	}
	for _, name := range []string{"traffic-chart.js", "traffic-chart.css"} {
		data, err := fs.ReadFile(assets, name)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"https://", "http://", "localStorage", "sessionStorage", "eval(", "new Function("} {
			if strings.Contains(string(data), banned) {
				t.Fatalf("%s runtime dependency or persistence: %s", name, banned)
			}
		}
	}
	app, _ := fs.ReadFile(assets, "app.js")
	for _, contract := range []string{"const monitorCoordinator = NetworkQualityCore.coordinator()", "coordinator: monitorCoordinator", "trafficCharts.sync(", "network.append(trafficCharts.mount(agent))", "networkQuality.close(); trafficCharts.close(); monitorCoordinator.close()"} {
		if !strings.Contains(string(app), contract) {
			t.Fatalf("missing shared lifecycle: %s", contract)
		}
	}
}
