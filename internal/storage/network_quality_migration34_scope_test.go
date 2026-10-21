package storage

import (
	"context"
	"path/filepath"
	"testing"
)

// Layer one covers a device that has a configuration, a slot row, or an OPEN interval. A device with
// nothing but ended history stays untouched; an included device still counts its ended intervals
// when it takes the next version (design 3.1).
func TestMigration34LayerOneScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scope.db")
	s := m34Open(t, path, false)
	_, _, targets := m34Catalog([]string{"telecom"}, 7)
	closed := m34Interval{"telecom", "ipv4", "tcp", 7, "telecomipv4", int64(4000), int64(7)}
	ids := map[string]string{
		"closed history only": m34Seed(t, s, m34Spec{name: "closed history only", noConfig: true, targets: targets, intervals: []m34Interval{closed}}, 1),
		"open interval only": m34Seed(t, s, m34Spec{name: "open interval only", noConfig: true, targets: targets, intervals: []m34Interval{
			{"telecom", "ipv4", "tcp", 9, "telecomipv4", int64(3000), int64(9)}, {"telecom", "ipv4", "icmp", 3, "telecomipv4", nil, nil}}}, 2),
		"slot only": m34Seed(t, s, m34Spec{name: "slot only", noConfig: true, targets: targets, slots: []m34Slot{{"telecom", "ipv4", "tcp", "telecomipv4", 4}}}, 3),
	}
	s.Close()
	withoutMigration34(t)
	migration34Skip = false
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows := func(id string) (n int, revision int64) {
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM quality_config WHERE agent_id=?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			if err := s.db.QueryRow(`SELECT revision FROM quality_config WHERE agent_id=?`, id).Scan(&revision); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	if n, _ := rows(ids["closed history only"]); n != 0 {
		t.Fatalf("a device with only ended history gained a configuration (%d rows)", n)
	}
	if n, _ := s.rowsOf(t, `SELECT COUNT(*) FROM quality_slot_settings WHERE agent_id=?`, ids["closed history only"]); n != 0 {
		t.Fatal("a device with only ended history gained slot settings")
	}
	if n, rev := rows(ids["open interval only"]); n != 1 || rev != 10 {
		t.Fatalf("open-interval device: rows=%d revision=%d (the highest version seen is 9, so 10)", n, rev)
	}
	if n, rev := rows(ids["slot only"]); n != 1 || rev != 5 {
		t.Fatalf("slot-only device: rows=%d revision=%d", n, rev)
	}
}

func (s *Store) rowsOf(t *testing.T, query string, args ...any) (int, error) {
	t.Helper()
	var n int
	err := s.db.QueryRow(query, args...).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n, nil
}
