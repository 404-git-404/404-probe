package collector

import (
	"errors"
	"testing"
)

func TestInterfaceFiltering(t *testing.T) {
	c := Collector{}
	for _, name := range []string{"lo", "docker0", "veth123", "br-test", "wg0", "tailscale0"} {
		if c.includeInterface(name) {
			t.Errorf("default included %s", name)
		}
	}
	if !c.includeInterface("eth0") {
		t.Error("eth0 should be included")
	}
	c = Collector{Includes: []string{"wg*"}}
	if !c.includeInterface("wg0") || c.includeInterface("eth0") {
		t.Error("explicit include was not honored")
	}
	c = Collector{Includes: []string{"wg*"}, Excludes: []string{"wg-test"}}
	if c.includeInterface("wg-test") {
		t.Error("explicit exclude should win")
	}
}

func TestBootIdentityDoesNotSwitchToFallback(t *testing.T) {
	responses := []struct {
		id  string
		err error
	}{{id: "boot-uuid"}, {err: errors.New("temporary read failure")}, {id: "boot-uuid"}}
	index := 0
	c := Collector{bootIDReader: func() (string, error) {
		response := responses[index]
		index++
		return response.id, response.err
	}}
	if id, err := c.bootID(); err != nil || id != "boot-uuid" {
		t.Fatalf("first identity=%q err=%v", id, err)
	}
	if _, err := c.bootID(); err == nil {
		t.Fatal("temporary boot ID failure should skip the sample")
	}
	if id, err := c.bootID(); err != nil || id != "boot-uuid" {
		t.Fatalf("recovered identity=%q err=%v", id, err)
	}
	restarted := Collector{bootIDReader: func() (string, error) { return "boot-uuid", nil }}
	if id, err := restarted.bootID(); err != nil || id != "boot-uuid" {
		t.Fatalf("restart identity=%q err=%v", id, err)
	}
}
