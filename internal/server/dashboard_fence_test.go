package server

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDashboardResetTriggerReadRemainsKnown(t *testing.T) {
	a, s, _, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	for i := 0; i < 2050; i++ {
		a.invalidateDashboardNetwork(fmt.Sprint("old", i))
	}
	before := a.dashboardResets
	if v := a.cachedDashboardNetwork(httptest.NewRequest("GET", "/", nil), "trigger", time.Now()); !v.Known {
		t.Fatal("reset-triggering read returned unknown")
	}
	if a.dashboardResets != before+1 {
		t.Fatal("map reset missing")
	}
}

func TestDashboardSameDeviceInvalidationFencesInflightRead(t *testing.T) {
	a, s, _, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	entered, release := make(chan struct{}), make(chan struct{})
	a.dashboardBeforeCommit = func(id string) {
		if id == "same" {
			close(entered)
			<-release
		}
	}
	result := make(chan dashboardNetwork, 1)
	go func() { result <- a.cachedDashboardNetwork(httptest.NewRequest("GET", "/", nil), "same", time.Now()) }()
	<-entered
	a.invalidateDashboardNetwork("same")
	close(release)
	if (<-result).Known {
		t.Fatal("same-device invalidation accepted old result")
	}
	if a.dashboardNetwork["same"].Value.Known {
		t.Fatal("old result entered cache")
	}
	a.dashboardBeforeCommit = nil
	if !a.cachedDashboardNetwork(httptest.NewRequest("GET", "/", nil), "same", time.Now()).Known {
		t.Fatal("quiet read failed")
	}
}

func TestDashboardResetFencesOtherInflightRead(t *testing.T) {
	a, s, _, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	entered, release := make(chan struct{}), make(chan struct{})
	a.dashboardBeforeCommit = func(id string) {
		if id == "old" {
			close(entered)
			<-release
		}
	}
	result := make(chan dashboardNetwork, 1)
	go func() { result <- a.cachedDashboardNetwork(httptest.NewRequest("GET", "/", nil), "old", time.Now()) }()
	<-entered
	for i := 0; i < 2050; i++ {
		a.invalidateDashboardNetwork(fmt.Sprint(i))
	}
	if !a.cachedDashboardNetwork(httptest.NewRequest("GET", "/", nil), "trigger", time.Now()).Known {
		t.Fatal("trigger read failed")
	}
	close(release)
	if (<-result).Known {
		t.Fatal("old epoch survived reset")
	}
	if _, ok := a.dashboardNetwork["old"]; ok {
		t.Fatal("old epoch refilled reset map")
	}
}
