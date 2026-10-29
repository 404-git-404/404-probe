package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"404-probe/internal/auth"
	"golang.org/x/text/cases"
	"golang.org/x/text/currency"
	"golang.org/x/text/unicode/norm"
)

var (
	ErrOrganizationInvalid      = errors.New("invalid organization input")
	ErrOrganizationConflict     = errors.New("organization revision conflict")
	ErrOrganizationNameConflict = errors.New("organization name already exists")
	ErrOrganizationNotFound     = errors.New("organization resource not found")
	ErrOrganizationLimit        = errors.New("organization limit reached")
	ErrExchangeRateReadOnly     = errors.New("USD exchange rate is fixed")
	organizationID              = regexp.MustCompile(`^[0-9a-f]{32}$`)
	exchangeDecimal             = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,12})?$`)
	isoCurrency                 = regexp.MustCompile(`^[A-Z]{3}$`)
)

const MaxOrganizationEntities = 200
const MaxAgentTags = 20

type AgentGroup struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	SortOrder int    `json:"sort_order"`
	Revision  int64  `json:"revision"`
}
type AgentTag struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Revision int64  `json:"revision"`
}
type AgentOrganization struct {
	AgentID  string   `json:"agent_id"`
	GroupID  *string  `json:"group_id"`
	TagIDs   []string `json:"tag_ids"`
	Revision int64    `json:"revision"`
}
type ExchangeRate struct {
	Currency    string `json:"currency"`
	UnitsPerUSD string `json:"units_per_usd"`
	UpdatedAt   int64  `json:"updated_at"`
	Revision    int64  `json:"revision"`
	Editable    bool   `json:"editable"`
}
type OrganizationCatalog struct {
	Revision    int64                        `json:"revision"`
	Groups      []AgentGroup                 `json:"groups"`
	Tags        []AgentTag                   `json:"tags"`
	Rates       []ExchangeRate               `json:"exchange_rates"`
	Assignments map[string]AgentOrganization `json:"-"`
}
type OrganizationChange struct {
	ExpectedRevision        int64
	Kind, ID, Name, AgentID string
	Order                   []string
	GroupID                 *string
	TagIDs                  []string
	NewGroup                string
	NewTags                 []string
	Rates                   []ExchangeRate
}
type OrganizationChangeResult struct {
	Revision        int64    `json:"revision"`
	ID              string   `json:"id,omitempty"`
	ChangedAgentIDs []string `json:"changed_agent_ids"`
}

func normalizeOrganizationName(name string, group bool) (string, string, error) {
	if !utf8.ValidString(name) {
		return "", "", ErrOrganizationInvalid
	}
	name = norm.NFC.String(strings.TrimSpace(name))
	limit := 20
	if group {
		limit = 40
	}
	if name == "" || utf8.RuneCountInString(name) > limit {
		return "", "", fmt.Errorf("%w: name length", ErrOrganizationInvalid)
	}
	for _, r := range name {
		if (unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)) && r != '\u200d' {
			return "", "", fmt.Errorf("%w: control character", ErrOrganizationInvalid)
		}
	}
	key := norm.NFC.String(cases.Fold().String(name))
	if group && key == "未分组" {
		return "", "", fmt.Errorf("%w: reserved ungrouped name", ErrOrganizationInvalid)
	}
	return name, key, nil
}

func ValidISOCurrency(code string) bool {
	if !isoCurrency.MatchString(code) {
		return false
	}
	_, err := currency.ParseISO(code)
	return err == nil
}
func NormalizeExchangeRate(code, value string) (string, error) {
	if code == "USD" {
		return "", ErrExchangeRateReadOnly
	}
	if !ValidISOCurrency(code) || !exchangeDecimal.MatchString(value) {
		return "", ErrOrganizationInvalid
	}
	r, ok := new(big.Rat).SetString(value)
	if !ok || r.Sign() <= 0 {
		return "", ErrOrganizationInvalid
	}
	if strings.Contains(value, ".") {
		value = strings.TrimRight(strings.TrimRight(value, "0"), ".")
	}
	return value, nil
}

func (s *Store) OrganizationCatalog(ctx context.Context) (OrganizationCatalog, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return OrganizationCatalog{}, err
	}
	defer tx.Rollback()
	c, err := organizationCatalogTx(ctx, tx)
	if err != nil {
		return c, err
	}
	return c, tx.Commit()
}
func organizationCatalogTx(ctx context.Context, tx *sql.Tx) (OrganizationCatalog, error) {
	c := OrganizationCatalog{Groups: []AgentGroup{}, Tags: []AgentTag{}, Rates: []ExchangeRate{}, Assignments: map[string]AgentOrganization{}}
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM organization_meta WHERE id=1`).Scan(&c.Revision); err != nil {
		return c, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,name,sort_order,revision FROM groups ORDER BY sort_order,id`)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var g AgentGroup
		if err = rows.Scan(&g.ID, &g.Name, &g.SortOrder, &g.Revision); err != nil {
			rows.Close()
			return c, err
		}
		c.Groups = append(c.Groups, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return c, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT id,name,revision FROM tags ORDER BY name_key,id`)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var t AgentTag
		if err = rows.Scan(&t.ID, &t.Name, &t.Revision); err != nil {
			rows.Close()
			return c, err
		}
		c.Tags = append(c.Tags, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return c, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT currency,units_per_usd,updated_at,revision FROM expense_exchange_rates ORDER BY CASE currency WHEN 'USD' THEN 0 ELSE 1 END,currency`)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var r ExchangeRate
		if err = rows.Scan(&r.Currency, &r.UnitsPerUSD, &r.UpdatedAt, &r.Revision); err != nil {
			rows.Close()
			return c, err
		}
		r.Editable = r.Currency != "USD"
		c.Rates = append(c.Rates, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return c, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT agent_id,group_id,revision FROM agent_organization`)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var a AgentOrganization
		var group sql.NullString
		if err = rows.Scan(&a.AgentID, &group, &a.Revision); err != nil {
			rows.Close()
			return c, err
		}
		if group.Valid {
			v := group.String
			a.GroupID = &v
		}
		a.TagIDs = []string{}
		c.Assignments[a.AgentID] = a
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return c, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT agent_id,tag_id FROM agent_tags ORDER BY agent_id,tag_id`)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var id, tag string
		if err = rows.Scan(&id, &tag); err != nil {
			rows.Close()
			return c, err
		}
		a, ok := c.Assignments[id]
		if !ok {
			a = AgentOrganization{AgentID: id, Revision: c.Revision, TagIDs: []string{}}
		}
		a.TagIDs = append(a.TagIDs, tag)
		c.Assignments[id] = a
	}
	err = rows.Err()
	rows.Close()
	return c, err
}

func organizationEntityExists(ctx context.Context, tx *sql.Tx, table, id string) error {
	if !organizationID.MatchString(id) {
		return ErrOrganizationInvalid
	}
	var found int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE id=?`, id).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrOrganizationNotFound
	}
	return err
}
func organizationNameError(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return ErrOrganizationNameConflict
	}
	return err
}
func createOrganizationEntity(ctx context.Context, tx *sql.Tx, group bool, name string, revision int64) (string, error) {
	name, key, err := normalizeOrganizationName(name, group)
	if err != nil {
		return "", err
	}
	table := "tags"
	if group {
		table = "groups"
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
		return "", err
	}
	if count >= MaxOrganizationEntities {
		return "", ErrOrganizationLimit
	}
	id, err := auth.NewID()
	if err != nil {
		return "", err
	}
	if group {
		_, err = tx.ExecContext(ctx, `INSERT INTO groups(id,name,name_key,sort_order,revision) VALUES(?,?,?,(SELECT COALESCE(max(sort_order),-1)+1 FROM groups),?)`, id, name, key, revision)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO tags(id,name,name_key,revision) VALUES(?,?,?,?)`, id, name, key, revision)
	}
	return id, organizationNameError(err)
}
func organizationAffected(ctx context.Context, tx *sql.Tx, query string, arg string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Every organization write starts with the same CAS, then commits all effects.
// No error (including a later FK/name error) consumes a revision or leaves an
// inline-created group/tag behind. Table names below are private constants.
func (s *Store) ChangeOrganization(ctx context.Context, c OrganizationChange, now time.Time) (OrganizationChangeResult, error) {
	out := OrganizationChangeResult{ChangedAgentIDs: []string{}}
	if c.ExpectedRevision < 1 {
		return out, ErrOrganizationInvalid
	}
	switch c.Kind {
	case "create_group", "rename_group", "delete_group", "sort_groups", "create_tag", "rename_tag", "delete_tag", "assign", "save_rates", "delete_rate":
	default:
		return out, ErrOrganizationInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	// At the JS exact-integer upper bound writes fail closed with conflict;
	// reaching it would require centuries of sustained organization writes.
	r, err := tx.ExecContext(ctx, `UPDATE organization_meta SET revision=revision+1 WHERE id=1 AND revision=? AND revision<9007199254740991`, c.ExpectedRevision)
	if err != nil {
		return out, err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return out, err
	}
	if n != 1 {
		return out, ErrOrganizationConflict
	}
	out.Revision = c.ExpectedRevision + 1
	switch c.Kind {
	case "create_group", "create_tag":
		out.ID, err = createOrganizationEntity(ctx, tx, c.Kind == "create_group", c.Name, out.Revision)
	case "rename_group", "rename_tag", "delete_group", "delete_tag":
		group := strings.HasSuffix(c.Kind, "group")
		table := "tags"
		if group {
			table = "groups"
		}
		if err = organizationEntityExists(ctx, tx, table, c.ID); err != nil {
			return out, err
		}
		out.ID = c.ID
		query := `SELECT agent_id FROM agent_tags WHERE tag_id=? ORDER BY agent_id`
		if group {
			query = `SELECT agent_id FROM agent_organization WHERE group_id=? ORDER BY agent_id`
		}
		out.ChangedAgentIDs, err = organizationAffected(ctx, tx, query, c.ID)
		if err != nil {
			return out, err
		}
		if strings.HasPrefix(c.Kind, "rename_") {
			name, key, nameErr := normalizeOrganizationName(c.Name, group)
			if nameErr != nil {
				return out, nameErr
			}
			_, err = tx.ExecContext(ctx, `UPDATE `+table+` SET name=?,name_key=?,revision=? WHERE id=?`, name, key, out.Revision, c.ID)
			err = organizationNameError(err)
		} else {
			for _, id := range out.ChangedAgentIDs {
				if _, err = tx.ExecContext(ctx, `INSERT INTO agent_organization(agent_id,revision) VALUES(?,?) ON CONFLICT(agent_id) DO UPDATE SET revision=excluded.revision`, id, out.Revision); err != nil {
					return out, err
				}
			}
			_, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE id=?`, c.ID)
		}
	case "sort_groups":
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM groups`).Scan(&count); err != nil {
			return out, err
		}
		if count != len(c.Order) {
			return out, ErrOrganizationInvalid
		}
		seen := map[string]bool{}
		for i, id := range c.Order {
			if seen[id] {
				return out, ErrOrganizationInvalid
			}
			seen[id] = true
			if err = organizationEntityExists(ctx, tx, "groups", id); err != nil {
				return out, err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE groups SET sort_order=?,revision=? WHERE id=?`, i, out.Revision, id); err != nil {
				return out, err
			}
		}
	case "assign":
		if !organizationID.MatchString(c.AgentID) || len(c.TagIDs)+len(c.NewTags) > MaxAgentTags {
			return out, ErrOrganizationInvalid
		}
		var revoked bool
		err = tx.QueryRowContext(ctx, `SELECT revoked FROM agents WHERE id=?`, c.AgentID).Scan(&revoked)
		if errors.Is(err, sql.ErrNoRows) {
			return out, ErrOrganizationNotFound
		}
		if err != nil {
			return out, err
		}
		if revoked {
			return out, ErrAgentRevoked
		}
		if c.NewGroup != "" {
			if c.GroupID != nil {
				return out, ErrOrganizationInvalid
			}
			id, createErr := createOrganizationEntity(ctx, tx, true, c.NewGroup, out.Revision)
			if createErr != nil {
				return out, createErr
			}
			c.GroupID = &id
		}
		if c.GroupID != nil {
			if err = organizationEntityExists(ctx, tx, "groups", *c.GroupID); err != nil {
				return out, err
			}
		}
		for _, name := range c.NewTags {
			id, createErr := createOrganizationEntity(ctx, tx, false, name, out.Revision)
			if createErr != nil {
				return out, createErr
			}
			c.TagIDs = append(c.TagIDs, id)
		}
		seen := map[string]bool{}
		for _, id := range c.TagIDs {
			if seen[id] {
				return out, ErrOrganizationInvalid
			}
			seen[id] = true
			if err = organizationEntityExists(ctx, tx, "tags", id); err != nil {
				return out, err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_organization(agent_id,group_id,revision) VALUES(?,?,?) ON CONFLICT(agent_id) DO UPDATE SET group_id=excluded.group_id,revision=excluded.revision`, c.AgentID, c.GroupID, out.Revision)
		if err != nil {
			return out, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM agent_tags WHERE agent_id=?`, c.AgentID); err != nil {
			return out, err
		}
		for _, id := range c.TagIDs {
			if _, err = tx.ExecContext(ctx, `INSERT INTO agent_tags(agent_id,tag_id) VALUES(?,?)`, c.AgentID, id); err != nil {
				return out, err
			}
		}
		out.ID = c.AgentID
		out.ChangedAgentIDs = []string{c.AgentID}
	case "save_rates":
		if len(c.Rates) == 0 || len(c.Rates) > 256 {
			return out, ErrOrganizationInvalid
		}
		seen := map[string]bool{}
		for _, rate := range c.Rates {
			value, validationErr := NormalizeExchangeRate(rate.Currency, rate.UnitsPerUSD)
			if validationErr != nil {
				return out, validationErr
			}
			if seen[rate.Currency] {
				return out, ErrOrganizationInvalid
			}
			seen[rate.Currency] = true
			if _, err = tx.ExecContext(ctx, `INSERT INTO expense_exchange_rates(currency,units_per_usd,updated_at,revision) VALUES(?,?,?,?) ON CONFLICT(currency) DO UPDATE SET units_per_usd=excluded.units_per_usd,updated_at=excluded.updated_at,revision=excluded.revision`, rate.Currency, value, now.UnixMilli(), out.Revision); err != nil {
				return out, err
			}
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM expense_exchange_rates`).Scan(&count); err != nil {
			return out, err
		}
		if count > 256 {
			return out, ErrOrganizationLimit
		}
	case "delete_rate":
		if c.ID == "USD" {
			return out, ErrExchangeRateReadOnly
		}
		if !ValidISOCurrency(c.ID) {
			return out, ErrOrganizationInvalid
		}
		r, deleteErr := tx.ExecContext(ctx, `DELETE FROM expense_exchange_rates WHERE currency=?`, c.ID)
		if deleteErr != nil {
			return out, deleteErr
		}
		n, _ := r.RowsAffected()
		if n != 1 {
			return out, ErrOrganizationNotFound
		}
		out.ID = c.ID
	}
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
