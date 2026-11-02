package updatecheck

import (
	"net/http"
	"testing"
	"time"
)

func TestWithoutWebAuthenticationMakesZeroRequests(t *testing.T) {
	fixture, count := testChecker(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected request") })
	c := NewDisabled(fixture.info, nil)
	defer c.Close()
	c.client.Transport = fixture.client.Transport
	c.fetchURL = fixture.fetchURL
	c.delay = func() time.Duration { return 0 }
	c.Start(t.Context())
	for i := 0; i < 20; i++ {
		if err := c.check(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if c.Snapshot().Enabled || count.Load() != 0 {
		t.Fatal(c.Snapshot(), count.Load())
	}
	if _, err := c.SetEnabled(true); err == nil {
		t.Fatal("inactive checker enabled")
	}
}
