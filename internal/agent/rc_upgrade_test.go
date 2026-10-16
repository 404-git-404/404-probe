package agent

import "testing"

func TestRCAgentCurrentVersionBoundary(t *testing.T) {
	for _, v := range []string{"v1.0.1-beta.2", "v1.0.1", "v1.0.2", "v1.0.3-beta.1", "dirty", "unknown"} {
		if rcVersionAware(v) {
			t.Fatal(v)
		}
	}
	for _, v := range []string{"v1.0.3-rc.1", "v1.0.3", "v1.0.4-beta.1"} {
		if !rcVersionAware(v) {
			t.Fatal(v)
		}
	}
}
