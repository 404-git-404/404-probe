package storage

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"404-probe/internal/auth"
)

func TestMigration49FromV141PreservesTargetsAndTags(t *testing.T) {
	ctx := t.Context()
	archive, err := os.ReadFile("testdata/schema48-v1.4.1.db.gz")
	if err != nil {
		t.Fatal(err)
	}
	r, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "upgrade.db")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	// The database was opened and closed by the unmodified v1.4.1 storage.Open.
	// Seed it before calling the current storage.Open, which performs the upgrade.
	db, err := sql.Open("sqlite", sqliteOpenDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if version, err := schemaVersion(ctx, db); err != nil || version != 48 {
		t.Fatalf("fixture schema %d: %v", version, err)
	}
	now := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	first, second := strings.Repeat("a", 32), strings.Repeat("b", 32)
	old := &Store{db: db}
	for _, id := range []string{first, second} {
		if err := old.AddAgent(ctx, id, "agent-"+id[:1], auth.Hash(id), now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO quality_config(agent_id,revision,settings_revision,enabled,ipv6,choices,updated_at,objects_configured)
			VALUES (?1,7,9,1,0,'[]',?3,1), (?2,7,9,1,0,'[]',?3,1);
		INSERT INTO quality_slot_settings(agent_id,slot,kind,region,isp,host,port,slot_enabled,ipv4_tcp,ipv4_icmp,updated_revision,position,show_on_card)
			VALUES (?1,'telecom','builtin','上海','telecom','Example.COM',443,1,1,1,9,0,1),
				(?2,'telecom','builtin','上海','telecom','example.com',443,1,1,0,9,0,1),
				(?2,'unicom','builtin','上海','unicom','example.com',443,0,0,0,9,1,0),
				(?1,'mobile','builtin','江苏','mobile','',NULL,0,0,0,9,1,0);
		INSERT INTO quality_targets(id,agent_id,fingerprint,payload,created_at,identity,created_revision)
			VALUES (?4,?1,?4,?5,?3,?4,7);
		INSERT INTO quality_slots(agent_id,slot,family,protocol,revision,target_id,enabled)
			VALUES(?1,'telecom','ipv4','tcp',7,?4,1);
		INSERT INTO quality_intervals(agent_id,slot,family,protocol,revision,target_id,start_at,config_from,v1_authorized)
			VALUES(?1,'telecom','ipv4','tcp',7,?4,?3,7,0);
		INSERT INTO tags(id,name,name_key,revision,color) VALUES(?6,'远程','远程',3,'teal');
		INSERT INTO agent_tags VALUES(?1,?6), (?2,?6);
	`, first, second, now.Add(-30*time.Minute).UnixMilli(), strings.Repeat("d", 32),
		`{"slot":"telecom","family":"ipv4","protocol":"tcp","source":"user","host":"example.com","port":443,"endpoint_version":"0123456789abcdef"}`, strings.Repeat("c", 32)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if version, err := s.SchemaVersion(ctx); err != nil || version != 49 {
		t.Fatal(version, err)
	}
	targets, err := s.ListNetworkTargets(ctx)
	if err != nil || len(targets) != 2 {
		t.Fatal(targets, err)
	}
	if t1, t2 := targets[0], targets[1]; t1.ISP != "电信" || len(t1.UsedBy) != 2 || t1.Host != "example.com" ||
		t2.ISP != "联通" || t2.Host != "example.com" || t1.ID == t2.ID || len(t2.UsedBy) != 1 {
		t.Fatal(targets)
	}
	var kind, isp, host, laneKey, laneTarget string
	var libraryID sql.NullString
	var enabled, tcp, icmp, shown bool
	var revision, position int
	if err := s.db.QueryRowContext(ctx, `SELECT kind,isp,host,library_id,slot_enabled,ipv4_tcp,ipv4_icmp,show_on_card,updated_revision,position
		FROM quality_slot_settings WHERE agent_id=? AND slot='telecom'`, first).Scan(&kind, &isp, &host, &libraryID, &enabled, &tcp, &icmp, &shown, &revision, &position); err != nil ||
		kind != "custom" || isp != "电信" || host != "Example.COM" || libraryID.String != targets[0].ID || !enabled || !tcp || !icmp || !shown || revision != 9 || position != 0 {
		t.Fatal(kind, isp, host, libraryID, enabled, tcp, icmp, shown, revision, position, err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT library_id FROM quality_slot_settings WHERE agent_id=? AND slot='mobile'`, first).Scan(&libraryID); err != nil || libraryID.Valid {
		t.Fatal(libraryID, err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT slot||'/'||family||'/'||protocol,target_id FROM quality_intervals WHERE agent_id=?`, first).
		Scan(&laneKey, &laneTarget); err != nil || laneKey != "telecom/ipv4/tcp" || laneTarget != strings.Repeat("d", 32) {
		t.Fatal(laneKey, laneTarget, err)
	}
	card, err := readDashboardNetworkSlots(ctx, s.db, now)
	if err != nil || card[first][0].TargetName != "上海电信" {
		t.Fatal(card, err)
	}
	fleet, err := s.NetworkFleet(ctx, first, "ipv4", "tcp", now.Add(-time.Hour).UnixMilli(), now.UnixMilli(), now, time.Minute)
	if err != nil || len(fleet.Machines) != 1 || len(fleet.Machines[0].Objects) == 0 {
		t.Fatal(fleet, err)
	}
	slot := fleet.Machines[0].Objects[0]
	if slot.Slot != "telecom" || slot.Kind != "builtin" || slot.Region+legacyQualityCarrier(slot.ISP) != "上海电信" {
		t.Fatal(slot)
	}
	var name, color string
	var count, fkErrors int
	err = s.db.QueryRowContext(ctx, `SELECT name,color,revision,(SELECT COUNT(*) FROM agent_tags),(SELECT COUNT(*) FROM pragma_foreign_key_check) FROM tags`).
		Scan(&name, &color, &revision, &count, &fkErrors)
	if err != nil || name != "远程" || color != "teal" || revision != 3 || count != 2 || fkErrors != 0 {
		t.Fatal(name, color, revision, count, fkErrors, err)
	}
	var defaultColor sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT dflt_value FROM pragma_table_info('tags') WHERE name='color'`).Scan(&defaultColor); err != nil || defaultColor.Valid {
		t.Fatal(defaultColor, err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM tags`); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_tags`).Scan(&count); err != nil || count != 0 {
		t.Fatal("tag FK cascade", count, err)
	}
}
