package storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

var organizationTables = []string{"agent_tags", "agent_organization", "tags", "groups", "expense_exchange_rates", "organization_meta"}

func organizationStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigration36TextPrimaryKeysRejectNull(t *testing.T) {
	s := organizationStore(t)
	for _, statement := range []string{
		`INSERT INTO groups(id,name,name_key,sort_order,revision) VALUES(NULL,'x','x',0,1)`,
		`INSERT INTO tags(id,name,name_key,revision) VALUES(NULL,'x','x',1)`,
		`INSERT INTO agent_organization(agent_id,revision) VALUES(NULL,1)`,
		`INSERT INTO expense_exchange_rates(currency,units_per_usd,updated_at,revision) VALUES(NULL,'1',0,1)`,
	} {
		if _, err := s.db.Exec(statement); err == nil {
			t.Fatal("SQLite rowid text primary key accepted NULL", statement)
		}
	}
}

func TestOrganizationSortHardLimitsAndSQLFailureRollback(t *testing.T) {
	s := organizationStore(t)
	id := strings.Repeat("a", 32)
	organizationAgent(t, s, id)
	a := organizationWrite(t, s, OrganizationChange{Kind: "create_group", Name: "A"})
	b := organizationWrite(t, s, OrganizationChange{Kind: "create_group", Name: "B"})
	organizationWrite(t, s, OrganizationChange{Kind: "sort_groups", Order: []string{b.ID, a.ID}})
	c := organizationRead(t, s)
	if c.Groups[0].ID != b.ID || c.Groups[1].SortOrder != 1 {
		t.Fatal(c.Groups)
	}
	if _, err := s.ChangeOrganization(t.Context(), OrganizationChange{ExpectedRevision: c.Revision, Kind: "sort_groups", Order: []string{a.ID, a.ID}}, time.Now()); err == nil {
		t.Fatal("nonpermutation accepted")
	}
	ids := []string{}
	for i := 0; i < 200; i++ {
		tag := fmt.Sprintf("%032x", i+1)
		ids = append(ids, tag)
		if _, err := s.db.Exec(`INSERT INTO tags(id,name,name_key,revision) VALUES(?,?,?,1)`, tag, fmt.Sprintf("tag-%03d", i), fmt.Sprintf("tag-%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	c = organizationRead(t, s)
	if _, err := s.ChangeOrganization(t.Context(), OrganizationChange{ExpectedRevision: c.Revision, Kind: "create_tag", Name: "over limit"}, time.Now()); !errors.Is(err, ErrOrganizationLimit) {
		t.Fatal(err)
	}
	if _, err := s.ChangeOrganization(t.Context(), OrganizationChange{ExpectedRevision: c.Revision, Kind: "assign", AgentID: id, TagIDs: ids[:21]}, time.Now()); !errors.Is(err, ErrOrganizationInvalid) {
		t.Fatal(err)
	}
	organizationWrite(t, s, OrganizationChange{Kind: "assign", AgentID: id, GroupID: &a.ID, TagIDs: ids[:20]})
	before := organizationRead(t, s)
	if _, err := s.db.Exec(`CREATE TRIGGER organization_injected_failure BEFORE INSERT ON agent_tags BEGIN SELECT RAISE(ABORT,'injected organization failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeOrganization(t.Context(), OrganizationChange{ExpectedRevision: before.Revision, Kind: "assign", AgentID: id, NewGroup: "rollback", TagIDs: ids[:2]}, time.Now()); err == nil {
		t.Fatal("injected SQL failure absent")
	}
	if !reflect.DeepEqual(before, organizationRead(t, s)) {
		t.Fatal("SQL failure lost old links or left new group")
	}
	if _, err := s.db.Exec(`DELETE FROM agents WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	after := organizationRead(t, s)
	if _, ok := after.Assignments[id]; ok {
		t.Fatal("agent deletion did not cascade")
	}
}

func TestDashboardStorageKeepsExpiredCycleAndNullPrices(t *testing.T) {
	s := organizationStore(t)
	id := strings.Repeat("a", 32)
	organizationAgent(t, s, id)
	if _, err := s.db.Exec(`INSERT INTO agent_plans(agent_id,traffic_mode,cycle_kind,cycle_count,cycle_anchor,timezone,usage_bytes,quota_bytes,cycle_start,cycle_end,purchase_price,renewal_price,updated_at) VALUES(?,'sum','monthly',1,'2020-01-01','UTC',80,100,1,2,NULL,'0',1)`, id); err != nil {
		t.Fatal(err)
	}
	data, err := s.ReadDashboardData(t.Context(), time.Now(), time.Minute, false)
	if err != nil || len(data.Records) != 1 {
		t.Fatal(data, err)
	}
	p := data.Records[0].Plan
	if p == nil || p.UsageBytes != 80 || p.CycleEnd == nil || *p.CycleEnd != 2 || p.PurchasePrice != "" || p.RenewalPrice != "0" {
		t.Fatal("read projected expired data to zero", p)
	}
}
func organizationAgent(t *testing.T, s *Store, id string) {
	t.Helper()
	h := sha256.Sum256([]byte(id))
	if err := s.AddAgent(context.Background(), id, id, h[:], time.Now()); err != nil {
		t.Fatal(err)
	}
}
func organizationRead(t *testing.T, s *Store) OrganizationCatalog {
	t.Helper()
	c, err := s.OrganizationCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func organizationWrite(t *testing.T, s *Store, c OrganizationChange) OrganizationChangeResult {
	t.Helper()
	if c.ExpectedRevision == 0 {
		c.ExpectedRevision = organizationRead(t, s).Revision
	}
	out, err := s.ChangeOrganization(context.Background(), c, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMigration36PreservesAllLegacyDataAndIsIdempotent(t *testing.T) {
	s := organizationStore(t)
	ctx := context.Background()
	periods := []any{"monthly", "quarterly", "semiannual", "yearly", "once", nil, ""}
	for i, period := range periods {
		id := strings.Repeat(string(rune('a'+i)), 32)
		organizationAgent(t, s, id)
		price := any("0")
		if i%2 == 1 {
			price = nil
		}
		if _, err := s.db.Exec(`INSERT INTO agent_plans(agent_id,purchase_price,purchase_price_period,renewal_price,renewal_price_period,updated_at) VALUES(?,?,?,?,?,1)`, id, price, period, price, period); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range organizationTables {
		if _, err := s.db.Exec(`DROP TABLE ` + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`DELETE FROM schema_migrations WHERE version=36`); err != nil {
		t.Fatal(err)
	}
	before := m35Fingerprint(t, s.db, "schema_migrations")
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	after := m35Fingerprint(t, s.db, append(organizationTables, "schema_migrations")...)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("migration changed legacy rows or schema")
	}
	first := m35Fingerprint(t, s.db)
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, m35Fingerprint(t, s.db)) {
		t.Fatal("repeat migration changed data")
	}
	if version, err := s.SchemaVersion(ctx); err != nil || version != 36 {
		t.Fatal(version, err)
	}
	c := organizationRead(t, s)
	if len(c.Groups) != 0 || len(c.Tags) != 0 || len(c.Rates) != 1 || c.Rates[0].UnitsPerUSD != "1" || c.Rates[0].Editable {
		t.Fatal(c)
	}
	if _, err := s.db.Exec(`UPDATE expense_exchange_rates SET units_per_usd='2' WHERE currency='USD'`); err == nil {
		t.Fatal("USD changed")
	}
	if _, err := s.db.Exec(`DELETE FROM expense_exchange_rates WHERE currency='USD'`); err == nil {
		t.Fatal("USD deleted")
	}
}

func TestMigration36FailureRollsBackEverything(t *testing.T) {
	for _, stage := range []string{"tables", "seed"} {
		t.Run(stage, func(t *testing.T) {
			s := organizationStore(t)
			for _, table := range organizationTables {
				if _, err := s.db.Exec(`DROP TABLE ` + table); err != nil {
					t.Fatal(err)
				}
			}
			s.db.Exec(`DELETE FROM schema_migrations WHERE version=36`)
			before := m35Fingerprint(t, s.db)
			injected := errors.New("injected migration 36 failure")
			migration36FailAt = func(at string) error {
				if at == stage {
					return injected
				}
				return nil
			}
			defer func() { migration36FailAt = nil }()
			if err := s.migrate(context.Background()); !errors.Is(err, injected) {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, m35Fingerprint(t, s.db)) {
				t.Fatal("failed migration left partial schema/data")
			}
			migration36FailAt = nil
			if err := s.migrate(context.Background()); err != nil {
				t.Fatal("retry", err)
			}
		})
	}
}

func TestOrganizationNamesTransactionsDeleteAndConcurrentCAS(t *testing.T) {
	s := organizationStore(t)
	id, other := strings.Repeat("a", 32), strings.Repeat("b", 32)
	organizationAgent(t, s, id)
	organizationAgent(t, s, other)
	g := organizationWrite(t, s, OrganizationChange{Kind: "create_group", Name: " Café "})
	before := organizationRead(t, s)
	if _, err := s.ChangeOrganization(context.Background(), OrganizationChange{ExpectedRevision: before.Revision, Kind: "create_group", Name: "CAFE\u0301"}, time.Now()); !errors.Is(err, ErrOrganizationNameConflict) {
		t.Fatal(err)
	}
	if organizationRead(t, s).Revision != before.Revision {
		t.Fatal("duplicate consumed CAS")
	}
	for _, name := range []string{"", "\n", "未分组", strings.Repeat("字", 41), "a\x00b"} {
		if _, _, err := normalizeOrganizationName(name, true); err == nil {
			t.Fatal("invalid group accepted", name)
		}
	}
	if _, _, err := normalizeOrganizationName(strings.Repeat("字", 20), false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := normalizeOrganizationName(strings.Repeat("字", 21), false); err == nil {
		t.Fatal("21 codepoints accepted")
	}
	tag := organizationWrite(t, s, OrganizationChange{Kind: "create_tag", Name: "Testing"})
	for _, agent := range []string{id, other} {
		organizationWrite(t, s, OrganizationChange{Kind: "assign", AgentID: agent, GroupID: &g.ID, TagIDs: []string{tag.ID}})
	}
	rename := organizationWrite(t, s, OrganizationChange{Kind: "rename_tag", ID: tag.ID, Name: "Renamed"})
	if len(rename.ChangedAgentIDs) != 2 {
		t.Fatal(rename)
	}
	before = organizationRead(t, s)
	_, err := s.ChangeOrganization(context.Background(), OrganizationChange{ExpectedRevision: before.Revision, Kind: "assign", AgentID: id, NewGroup: "must rollback", NewTags: []string{"also rollback"}, TagIDs: []string{strings.Repeat("f", 32)}}, time.Now())
	if !errors.Is(err, ErrOrganizationNotFound) || !reflect.DeepEqual(before, organizationRead(t, s)) {
		t.Fatal("partial inline transaction", err)
	}
	organizationWrite(t, s, OrganizationChange{Kind: "delete_group", ID: g.ID})
	if a := organizationRead(t, s).Assignments[id]; a.GroupID != nil || len(a.TagIDs) != 1 {
		t.Fatal(a)
	}
	organizationWrite(t, s, OrganizationChange{Kind: "delete_tag", ID: tag.ID})
	for _, a := range organizationRead(t, s).Assignments {
		if len(a.TagIDs) != 0 {
			t.Fatal(a)
		}
	}
	before = organizationRead(t, s)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, name := range []string{"one", "two"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, err := s.ChangeOrganization(context.Background(), OrganizationChange{ExpectedRevision: before.Revision, Kind: "create_tag", Name: name}, time.Now())
			results <- err
		}(name)
	}
	wg.Wait()
	close(results)
	ok, conflicts := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, ErrOrganizationConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflicts != 1 {
		t.Fatal(ok, conflicts)
	}
}

func TestOrganizationRatesLimitsAndForeignKeys(t *testing.T) {
	s := organizationStore(t)
	for _, v := range []string{"", "0", "-1", "NaN", "Infinity", "1e2", "01", ".1", "1000000000000", "0.0000000000001"} {
		if _, err := NormalizeExchangeRate("CNY", v); err == nil {
			t.Fatal(v)
		}
	}
	if _, err := NormalizeExchangeRate("ZZZ", "1"); err == nil {
		t.Fatal("unknown ISO accepted")
	}
	if _, err := NormalizeExchangeRate("USD", "1"); !errors.Is(err, ErrExchangeRateReadOnly) {
		t.Fatal(err)
	}
	organizationWrite(t, s, OrganizationChange{Kind: "save_rates", Rates: []ExchangeRate{{Currency: "CNY", UnitsPerUSD: "7.100"}, {Currency: "EUR", UnitsPerUSD: "0.93"}}})
	c := organizationRead(t, s)
	if c.Rates[1].Currency != "CNY" || c.Rates[1].UnitsPerUSD != "7.1" {
		t.Fatal(c.Rates)
	}
	_, err := s.ChangeOrganization(context.Background(), OrganizationChange{ExpectedRevision: c.Revision, Kind: "save_rates", Rates: []ExchangeRate{{Currency: "JPY", UnitsPerUSD: "150"}, {Currency: "EUR", UnitsPerUSD: "0"}}}, time.Now())
	if err == nil || !reflect.DeepEqual(c, organizationRead(t, s)) {
		t.Fatal("bulk rates partially committed")
	}
	organizationWrite(t, s, OrganizationChange{Kind: "delete_rate", ID: "CNY"})
	for _, q := range []string{`INSERT INTO agent_tags(agent_id,tag_id) VALUES('missing','missing')`, `INSERT INTO agent_organization(agent_id,revision) VALUES('missing',1)`} {
		if _, err := s.db.Exec(q); err == nil {
			t.Fatal("FK allowed orphan")
		}
	}
}
