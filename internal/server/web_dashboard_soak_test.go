package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

// All additions are private fixture data. The auth/report and quality ingest
// handlers are production handlers; targets are literals, no probes run here.
func seedDashboardSoak(t *testing.T, a *App, s *storage.Store, ids []string, tokens map[string]string, now time.Time) map[string]protocol.QualityV4Config {
	cfgs := map[string]protocol.QualityV4Config{}
	change := func(c storage.OrganizationChange) storage.OrganizationChangeResult {
		cat, err := s.OrganizationCatalog(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		c.ExpectedRevision = cat.Revision
		out, err := s.ChangeOrganization(t.Context(), c, now)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	groups := []string{}
	for i := 0; i < 4; i++ {
		groups = append(groups, change(storage.OrganizationChange{Kind: "create_group", Name: fmt.Sprintf("性能组%d%s", i, strings.Repeat("长", i*8))}).ID)
	}
	tags := []string{}
	for i := 0; i < 20; i++ {
		tags = append(tags, change(storage.OrganizationChange{Kind: "create_tag", Name: fmt.Sprintf("标签%02d%s", i, strings.Repeat("长", i%4*4))}).ID)
	}
	change(storage.OrganizationChange{Kind: "save_rates", Rates: []storage.ExchangeRate{{Currency: "EUR", UnitsPerUSD: "0.92"}, {Currency: "CNY", UnitsPerUSD: "7.1"}}})
	for i, id := range ids {
		var group *string
		if i%5 != 4 {
			group = &groups[i%4]
		}
		assigned := tags[:i%4]
		if i == 0 {
			assigned = tags
		}
		change(storage.OrganizationChange{Kind: "assign", AgentID: id, GroupID: group, TagIDs: assigned})
		currency := []string{"USD", "EUR", "CNY", "JPY"}[i%4]
		period := []string{"monthly", "quarterly", "semiannual", "yearly"}[i%4]
		zone := []string{"UTC", "America/Los_Angeles", "Pacific/Kiritimati"}[i%3]
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		date := now.In(loc).AddDate(0, 0, []int{0, 7, -1, 30}[i%4]).Format("2006-01-02")
		plan := storage.AgentPlan{AgentID: id, Currency: currency, RenewalPrice: fmt.Sprint(12 + i%19), RenewalPricePeriod: period, RenewalDate: date, Timezone: zone, TrafficMode: "sum", CycleKind: "monthly", CycleCount: 1, CycleAnchor: now.Format("2006-01-02"), QuotaValue: "100", QuotaUnit: "GB"}
		if i%11 == 0 {
			plan.RenewalPrice = ""
		}
		if i%13 == 0 {
			plan.CycleAnchor = now.AddDate(0, -3, 0).Format("2006-01-02")
		}
		if _, err := s.PutAgentPlan(t.Context(), plan, nil, now); err != nil {
			t.Fatal(err)
		}
		cfg, err := s.GetQualityConfigV4(t.Context(), id, now)
		if err != nil {
			t.Fatal(err)
		}
		cfgs[id] = cfg
		if i%5 == 0 {
			continue
		} // complete lack of recent quality samples is deliberate.
		samples := []protocol.QualitySample{}
		seq := int64(1)
		for m := -90; m < 0; m++ {
			for laneIndex, slot := range protocol.QualitySlotOrder() {
				at := now.Truncate(time.Minute).Add(time.Duration(m) * time.Minute)
				outcome, value := "success", float64(20+i%25)
				if i%3 == 0 {
					outcome = "timeout"
					value = 2500
				}
				sample := userTCPSample(t, cfg, slot, seq, at, value, outcome)
				normalizeSoakSample(&sample, value)
				seq++
				sample.ResolvedIP = []string{"8.8.8.8", "1.1.1.1", "9.9.9.9"}[laneIndex]
				samples = append(samples, sample)
				if len(samples) == 24 {
					saveSoakQuality(t, a, tokens[id], samples)
					samples = nil
				}
			}
		}
		if len(samples) > 0 {
			saveSoakQuality(t, a, tokens[id], samples)
		}
	}
	return cfgs
}

func normalizeSoakSample(sample *protocol.QualitySample, value float64) {
	d, _ := protocol.DecodeQualityDetails(sample.Details)
	d.DNSMS = 0
	d.DNSOutcome = "literal"
	d.ConnectMS = &value
	d.ConnectAttempted = true
	d.ConnectComplete = true
	sample.Details = d.Encode()
}

func saveSoakQuality(t *testing.T, a *App, token string, samples []protocol.QualitySample) {
	t.Helper()
	w := postWithHeaders(t, a, token, "/api/v1/agent/network-quality", protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: "session", Samples: samples}, userHeaders)
	var out protocol.QualityBatchResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, ack := range out.Results {
		if ack[2] != "committed" {
			t.Fatal("quality fixture not committed", ack)
		}
	}
}

func dashboardSoak(t *testing.T, a *App, server *httptest.Server, ids []string, tokens map[string]string, cfgs map[string]protocol.QualityV4Config) {
	root := os.Getenv("V110_SOAK_ROOT")
	if root == "" {
		return
	}
	if !filepath.IsAbs(root) {
		t.Fatal("absolute soak root required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	a.now = time.Now
	a.webAuth.publicOrigin, _ = url.Parse(server.URL)
	a.webAuth.secureCookie = false
	write := func(name string, v any) {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(root, name)
		for n := 0; n < 100; n++ {
			if err = os.WriteFile(file+".tmp", raw, 0600); err == nil {
				err = os.Rename(file+".tmp", file)
			}
			if err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal(err)
	}
	n := len(ids)
	prefix := strconv.Itoa(n)
	seq := make([]uint64, n)
	qualitySeq := make([]int64, n)
	for i := range seq {
		seq[i] = 1
		qualitySeq[i] = 100000
	}
	client := &http.Client{Timeout: 5 * time.Second}
	post := func(i int, path string, value any) error {
		body, _ := json.Marshal(value)
		r, _ := http.NewRequest("POST", server.URL+path, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+tokens[ids[i]])
		r.Header.Set("Content-Type", "application/json")
		for k, v := range userHeaders {
			r.Header.Set(k, v)
		}
		res, err := client.Do(r)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return fmt.Errorf("report status %d", res.StatusCode)
		}
		return nil
	}
	report := func(i int) error {
		seq[i]++
		r := userReport(ids[i], seq[i])
		r.AgentVersion = "v1.0.9"
		r.CollectedAt = time.Now().UnixMilli()
		r.CPUPercent = float64((i + int(seq[i])) % 70)
		if i%9 == 0 {
			r.CPUPercent = 94
		}
		r.RAMPercent = float64(20 + i%60)
		return post(i, "/api/v1/report", r)
	}
	for i := range ids {
		if err := report(i); err != nil {
			t.Fatal(err)
		}
	}
	write("ready-"+prefix+".json", map[string]any{"base": server.URL, "devices": n, "id": ids[0], "pid": os.Getpid()})
	defer os.Remove(filepath.Join(root, "ready-"+prefix+".json"))
	ticker := time.NewTicker(10 * time.Second / time.Duration(n))
	defer ticker.Stop()
	poll := time.NewTicker(50 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(55 * time.Minute)
	defer deadline.Stop()
	enabled := false
	index := 0
	last := ""
	reports := 0
	started := time.Time{}
	for {
		select {
		case <-deadline.C:
			t.Fatal("soak driver deadline")
		case <-t.Context().Done():
			return
		case <-ticker.C:
			if !enabled {
				continue
			}
			i := index % n
			index++
			if err := report(i); err != nil {
				t.Fatal(err)
			}
			reports++
			if seq[i]%3 == 0 && i%5 != 0 {
				samples := []protocol.QualitySample{}
				for laneIndex, slot := range protocol.QualitySlotOrder() {
					outcome, value := "success", float64(20+i%25)
					if i%3 == 0 {
						outcome = "timeout"
						value = 2500
					}
					qualitySeq[i]++
					sample := userTCPSample(t, cfgs[ids[i]], slot, qualitySeq[i], time.Now().Add(-3*time.Second), value, outcome)
					normalizeSoakSample(&sample, value)
					sample.ResolvedIP = []string{"8.8.8.8", "1.1.1.1", "9.9.9.9"}[laneIndex]
					samples = append(samples, sample)
				}
				if err := post(i, "/api/v1/agent/network-quality", protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: "session", Samples: samples}); err != nil {
					t.Fatal(err)
				}
			}
		case <-poll.C:
			raw, err := os.ReadFile(filepath.Join(root, "command-"+prefix+".json"))
			if err != nil {
				continue
			}
			var cmd struct{ Command, Nonce string }
			if json.Unmarshal(raw, &cmd) != nil || cmd.Nonce == last {
				continue
			}
			last = cmd.Nonce
			switch cmd.Command {
			case "start":
				enabled = true
				started = time.Now()
			case "pause":
				enabled = false
			case "done":
				a.hub.mu.Lock()
				overflows := a.hub.overflowDisconnects
				a.hub.mu.Unlock()
				write("ack-"+prefix+".json", map[string]any{"nonce": cmd.Nonce, "reports": reports, "seconds": time.Since(started).Seconds(), "sse_overflow_disconnects": overflows})
				return
			case "burst":
				enabled = false
				a.Shutdown()
				server.CloseClientConnections()
				address := server.Listener.Addr().String()
				server.Close()
				next, err := NewApp(a.store, 30*time.Second, nil, WithWebAuthentication(WebAuthenticationConfig{PasswordHash: a.webAuth.passwordHash, PublicOrigin: server.URL, AllowInsecureHTTP: true}))
				if err != nil {
					t.Fatal(err)
				}
				defer next.Shutdown()
				listener, err := net.Listen("tcp", address)
				if err != nil {
					t.Fatal(err)
				}
				replacement := httptest.NewUnstartedServer(next.Handler())
				replacement.Listener.Close()
				replacement.Listener = listener
				replacement.Start()
				defer replacement.Close()
				a = next
				server = replacement
				var wg sync.WaitGroup
				fail := make(chan error, n)
				for i := range ids {
					wg.Go(func() {
						if err := report(i); err != nil {
							fail <- err
						}
					})
				}
				wg.Wait()
				close(fail)
				for err := range fail {
					t.Fatal(err)
				}
				reports += n
				enabled = true
			}
			write("ack-"+prefix+".json", map[string]any{"nonce": cmd.Nonce, "reports": reports, "base": server.URL, "seconds": time.Since(started).Seconds()})
		}
	}
}
