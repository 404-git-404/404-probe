package storage

import (
	"404-probe/internal/protocol"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestM09CardWindowAndSavedChoices(t *testing.T) {
	s, id, old, now := qualityFixture(t, ":memory:")
	caps := a1Capabilities()
	caps.Semantics = protocol.QualityStagedSemantics
	a1Report(t, s, id, 2, now, caps, "debian 13")
	u := a1LaneConfig(old.Revision)
	cfg, err := s.UpdateQualityConfigV2(context.Background(), id, u, qualityTestResolver, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	seq := int64(1)
	output := map[string]any{}
	for _, target := range cfg.Targets {
		if target.Status != "active" {
			continue
		}
		var samples []protocol.QualitySample
		first := now.UnixMilli()/300000*300000 - 11*300000
		for i := 0; i < 12; i++ {
			at := time.UnixMilli(first + int64(i)*300000 + 10000)
			if i == 11 || at.After(now.Add(-3 * time.Second)) {
				at = now.Add(-3 * time.Second)
			}
			var sample protocol.QualitySample
			if target.Protocol == "tcp" {
				sample = a2TCP(target, cfg.Revision, seq, at, []float64{149, 150, 250, 251}[i%4], "success")
			} else {
				values := []float64{10, 20, 30}
				if i%4 > 0 {
					values[2] = -1
				}
				sample = a2Echo(target, cfg.Revision, seq, at, values)
			}
			samples = append(samples, sample)
			seq++
		}
		ack, e := s.SaveQualityBatch(context.Background(), id, protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: "quality_session", Samples: samples}, now)
		if e != nil {
			t.Fatal(e)
		}
		for _, result := range ack.Results {
			if result[2] != "committed" {
				t.Fatal(result)
			}
		}
		h, e := s.QualityCardHistory(context.Background(), id, target.ID, now)
		if e != nil || h.ResolutionMS != 300000 || len(h.Groups) != 1 || len(h.Groups[0].Points) != 12 {
			t.Fatal(h, e)
		}
		if h.Groups[0].Window.Count != 12 || h.WindowStart != first {
			t.Fatal(h)
		}
		if h.Groups[0].Points[0].ConfigMin != cfg.Revision || h.Groups[0].Points[0].SlotMin != target.SlotRevision {
			t.Fatal("identity", h)
		}
		if target.Protocol == "tcp" {
			closeA2(t, h.Groups[0].Points[0].P95, 149)
		} else if h.Groups[0].Points[1].Sent != 3 || h.Groups[0].Points[1].Received != 2 {
			t.Fatal("denominator", h)
		}
		output[target.ID] = h
	}
	card, e := s.QualityCardConfiguration(context.Background(), id)
	if e != nil || len(card.CardLanes) != 12 || card.RegionID != "region-a" {
		t.Fatal(card, e)
	}
	for _, lane := range card.CardLanes {
		if lane.Source != "catalog" || lane.Host != "127.0.0.1" || !lane.Enabled {
			t.Fatal("saved choices changed", lane)
		}
	}
	output["config"] = card
	if root := os.Getenv("M09_FIXTURE_OUTPUT"); root != "" {
		wire, e := json.MarshalIndent(output, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(root, "actual-sqlite-fixture.json"), wire, 0600); e != nil {
			t.Fatal(e)
		}
	}
}
