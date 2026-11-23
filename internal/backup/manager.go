package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"404-probe/internal/storage"
	"filippo.io/age"
)

var (
	ErrBusy      = errors.New("a backup operation is running")
	ErrDisabled  = errors.New("enable backups and save an age key before running a backup")
	errR2Network = errors.New("R2 network connection failed")
)

type Settings struct {
	R2Config
	Enabled   bool   `json:"enabled"`
	Keep      int    `json:"keep"`
	DailyAt   string `json:"daily_at"`
	Recipient string `json:"recipient"`
}

type Edit struct {
	Settings
	KeySaved bool   `json:"key_saved"`
	Revision uint64 `json:"revision"`
}

type Status struct {
	Destination
	Enabled          bool   `json:"enabled"`
	CredentialsSaved bool   `json:"credentials_saved"`
	RecipientSaved   bool   `json:"recipient_saved"`
	Keep             int    `json:"keep"`
	DailyAt          string `json:"daily_at"`
	Revision         uint64 `json:"revision"`
	Running          bool   `json:"running"`
	LastSuccess      int64  `json:"last_success,omitempty"`
	LastSize         int64  `json:"last_size,omitempty"`
	LastFailure      int64  `json:"last_failure,omitempty"`
	Reason           string `json:"reason,omitempty"`
	Next             int64  `json:"next,omitempty"`
	ObjectCount      *int   `json:"object_count"`
	Alert            bool   `json:"alert"`
	Overdue          bool   `json:"overdue"`
}

type state struct {
	Settings
	Revision     uint64 `json:"revision"`
	EnabledSince int64  `json:"enabled_since"`
	Scheduled    int64  `json:"scheduled"`
	LastSuccess  int64  `json:"last_success"`
	LastSize     int64  `json:"last_size"`
	LastFailure  int64  `json:"last_failure"`
	Reason       string `json:"reason"`
	Failed       bool   `json:"failed"`
	ObjectCount  *int   `json:"object_count"`
}

// Manager runs one backup job at a time and holds its lock only for short
// state transitions. Network and archive work never holds the Store writer.
type Manager struct {
	store       *storage.Store
	configPaths map[string]string
	logger      *slog.Logger
	mu          sync.Mutex
	state       state
	running     bool
	testing     bool
	jobs        chan Settings
	wake        chan struct{}
	clock       func() time.Time
	retryDelay  []time.Duration
}

func NewManager(ctx context.Context, store *storage.Store, configPaths map[string]string, logger *slog.Logger) (*Manager, error) {
	m := &Manager{
		store: store, configPaths: configPaths, logger: logger, jobs: make(chan Settings, 1), wake: make(chan struct{}, 1),
		clock: time.Now, retryDelay: []time.Duration{2 * time.Second, 8 * time.Second},
		state: state{Settings: Settings{Keep: 30, DailyAt: "04:00"}},
	}
	raw, err := store.ReadBackupState(ctx)
	if err != nil || len(raw) != 0 && json.Unmarshal(raw, &m.state) != nil {
		return nil, errors.New("could not read backup settings")
	}
	return m, nil
}

func (m *Manager) view(now time.Time) Status {
	s := m.state
	v := Status{
		Destination: s.Destination, Enabled: s.Enabled, CredentialsSaved: s.configured(), RecipientSaved: s.Recipient != "",
		Keep: s.Keep, DailyAt: s.DailyAt, Revision: s.Revision, Running: m.running, LastSuccess: s.LastSuccess,
		LastSize: s.LastSize, LastFailure: s.LastFailure, Reason: s.Reason, ObjectCount: s.ObjectCount,
	}
	if s.Enabled {
		previous, next := dailySlots(now, s.DailyAt)
		if !m.running && due(s, previous) {
			next = now
		}
		v.Next = next.UnixMilli()
		v.Overdue = now.Sub(time.UnixMilli(max(s.LastSuccess, s.EnabledSince))) > 48*time.Hour
		v.Alert = s.Failed || v.Overdue
	}
	return v
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.view(m.clock())
}

func dailySlots(now time.Time, daily string) (previous, next time.Time) {
	hour, _ := strconv.Atoi(daily[:2])
	minute, _ := strconv.Atoi(daily[3:])
	day := func(offset int) time.Time {
		local := now.AddDate(0, 0, offset)
		year, month, date := local.Date()
		at := time.Date(year, month, date, hour, minute, 0, 0, now.Location())
		// In a spring-forward gap Go may choose an earlier wall time.
		// Use the first valid minute at or after the requested local time.
		for at.Day() == date && at.Hour()*60+at.Minute() < hour*60+minute {
			at = at.Add(time.Minute)
		}
		return at
	}
	today := day(0)
	if now.Before(today) {
		return day(-1), today
	}
	return today, day(1)
}

func due(s state, previous time.Time) bool {
	return s.Enabled && previous.UnixMilli() >= s.EnabledSince && previous.UnixMilli() > s.Scheduled
}

func (m *Manager) save(ctx context.Context, next state) error {
	encoded, err := json.Marshal(next)
	if err != nil || m.store.WriteBackupState(ctx, encoded) != nil {
		return errors.New("could not save backup settings or status")
	}
	m.state = next
	return nil
}

// mergeCredentials permits blank write-only fields for an existing endpoint.
// Changing the endpoint requires new credentials so an older secret cannot be
// used to sign a request to a newly supplied host.
func mergeCredentials(candidate *R2Config, saved R2Config) error {
	if candidate.AccessKey == "" && candidate.SecretKey == "" && candidate.Endpoint == saved.Endpoint {
		candidate.Credentials = saved.Credentials
	}
	if (candidate.AccessKey == "") != (candidate.SecretKey == "") {
		return errors.New("enter both R2 credential fields to change them")
	}
	return nil
}

func (m *Manager) Save(ctx context.Context, edit Edit) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.state
	if m.running || m.testing || edit.Revision != current.Revision {
		return Status{}, ErrBusy
	}
	if edit.Keep < 7 || edit.Keep > 90 {
		return Status{}, errors.New("backup retention must be between 7 and 90 copies")
	}
	if _, err := time.Parse("15:04", edit.DailyAt); err != nil || len(edit.DailyAt) != 5 {
		return Status{}, errors.New("backup schedule must be in HH:MM format")
	}
	if err := mergeCredentials(&edit.R2Config, current.R2Config); err != nil {
		return Status{}, err
	}
	if edit.Enabled || edit.R2Config != (R2Config{}) {
		if _, err := edit.R2Config.normalize(); err != nil {
			return Status{}, err
		}
	}
	if edit.Recipient == "" {
		edit.Recipient = current.Recipient
	}
	if edit.Recipient != current.Recipient {
		if _, err := age.ParseX25519Recipient(edit.Recipient); err != nil || !edit.KeySaved {
			return Status{}, errors.New("save the age private key and confirm before changing the recipient")
		}
	}
	if edit.Enabled && edit.Recipient == "" {
		return Status{}, ErrDisabled
	}
	now := m.clock()
	next := current
	next.Settings = edit.Settings
	next.Revision++
	if current.R2Config != next.R2Config {
		next.ObjectCount = nil
	}
	if current.Destination != next.Destination {
		next.LastSuccess, next.LastSize = 0, 0
	}
	if next.Enabled && (!current.Enabled || current.Destination != next.Destination || current.DailyAt != next.DailyAt) {
		next.EnabledSince = now.UnixMilli()
		next.Scheduled = 0
	}
	if err := m.save(ctx, next); err != nil {
		return Status{}, err
	}
	select {
	case m.wake <- struct{}{}:
	default:
	}
	return m.view(now), nil
}

func (m *Manager) Test(ctx context.Context, config R2Config) error {
	m.mu.Lock()
	if m.testing || m.running {
		m.mu.Unlock()
		return ErrBusy
	}
	saved := m.state.R2Config
	if err := mergeCredentials(&config, saved); err != nil {
		m.mu.Unlock()
		return err
	}
	m.testing = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.testing = false
		m.mu.Unlock()
	}()
	client, err := newR2(config)
	if err != nil {
		return err
	}
	testCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	return client.test(testCtx)
}

func (m *Manager) Start() (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running || m.testing {
		return Status{}, ErrBusy
	}
	if !m.state.Enabled {
		return Status{}, ErrDisabled
	}
	m.running = true
	m.jobs <- m.state.Settings
	return m.view(m.clock()), nil
}

func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	var countChecked time.Time
	for ctx.Err() == nil {
		now := m.clock()
		if job, ok := m.scheduled(ctx, now); ok {
			m.run(ctx, job)
			countChecked = m.clock()
		}
		if now.Sub(countChecked) >= time.Hour {
			m.refreshCount(ctx)
			countChecked = m.clock()
		}
		select {
		case <-ctx.Done():
			return
		case job := <-m.jobs:
			m.run(ctx, job)
			countChecked = m.clock()
		case <-m.wake:
			countChecked = time.Time{}
		case <-ticker.C:
		}
	}
}

func (m *Manager) scheduled(ctx context.Context, now time.Time) (Settings, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	previous, _ := dailySlots(now, m.state.DailyAt)
	if m.running || m.testing || !due(m.state, previous) {
		return Settings{}, false
	}
	next := m.state
	next.Scheduled = previous.UnixMilli()
	if m.save(ctx, next) != nil {
		m.logger.Warn("could not record scheduled offsite backup")
		return Settings{}, false
	}
	m.running = true
	return next.Settings, true
}

func (m *Manager) refreshCount(ctx context.Context) {
	m.mu.Lock()
	config := m.state.R2Config
	busy := m.running || m.testing
	m.mu.Unlock()
	if busy || !config.configured() {
		return
	}
	client, err := newR2(config)
	if err != nil {
		return
	}
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	objects, err := client.listOwned(listCtx)
	if err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.R2Config == config {
		count := len(objects)
		next := m.state
		if next.ObjectCount == nil || *next.ObjectCount != count {
			next.ObjectCount = &count
			_ = m.save(ctx, next)
		}
	}
}

func (m *Manager) run(parent context.Context, settings Settings) {
	ctx, cancel := context.WithTimeout(parent, time.Hour)
	defer cancel()
	result := m.execute(ctx, settings)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running = false
	if parent.Err() != nil {
		return
	}
	next := m.state
	now := m.clock().UnixMilli()
	if result.size > 0 {
		next.LastSuccess, next.LastSize = now, result.size
	}
	if result.count != nil {
		next.ObjectCount = result.count
	}
	next.Failed = result.err != nil
	if next.Failed {
		next.LastFailure, next.Reason = now, result.err.Error()
		m.logger.Warn("offsite backup failed", "reason", next.Reason)
	}
	if m.save(parent, next) != nil {
		m.state = next
		m.logger.Warn("could not persist offsite backup status")
	}
}

type jobResult struct {
	size  int64
	count *int
	err   error
}

func (m *Manager) execute(ctx context.Context, settings Settings) (result jobResult) {
	client, err := newR2(settings.R2Config)
	if err != nil {
		return jobResult{err: err}
	}
	dir, err := os.MkdirTemp(m.store.StateDirectory(), ".offsite-backup-")
	if err != nil {
		return jobResult{err: errors.New("could not create backup working directory")}
	}
	defer func() {
		if os.RemoveAll(dir) != nil {
			result.err = errors.Join(result.err, errors.New("could not remove backup working files"))
		}
	}()
	snapshotCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	archive, err := stageArchive(snapshotCtx, m.store, dir, m.configPaths, settings.Recipient)
	cancel()
	if err != nil {
		return jobResult{err: fmt.Errorf("prepare backup: %w", err)}
	}
	key := client.prefix() + archiveName(m.clock(), archive.schema)
	if err := m.retry(ctx, 18*time.Minute, func(attempt context.Context) error {
		file, err := os.Open(archive.path)
		if err != nil {
			return errors.New("could not reopen encrypted backup working file")
		}
		defer file.Close()
		return client.put(attempt, key, file, archive.size, archive.digest, true)
	}); err != nil {
		return jobResult{err: fmt.Errorf("upload backup: %w", err)}
	}
	result.size = archive.size
	var count int
	if err := m.retry(ctx, time.Minute, func(attempt context.Context) error {
		var err error
		count, err = client.prune(attempt, settings.Keep, key)
		return err
	}); err != nil {
		result.err = fmt.Errorf("apply R2 retention: %w", err)
		return result
	}
	result.count = &count
	return result
}

func (m *Manager) retry(ctx context.Context, limit time.Duration, run func(context.Context) error) error {
	for i := 0; ; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		attempt, cancel := context.WithTimeout(ctx, limit)
		err := run(attempt)
		cancel()
		var status r2Error
		transient := errors.Is(err, errR2Network) || errors.Is(err, context.DeadlineExceeded) ||
			errors.As(err, &status) && (status == 408 || status == 429 || status >= 500)
		if err == nil || !transient || i == len(m.retryDelay) {
			return err
		}
		timer := time.NewTimer(m.retryDelay[i])
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// StoredR2 reads saved credentials for the root-only recovery command.
func StoredR2(ctx context.Context, database string) (R2Config, error) {
	raw, err := storage.ReadBackupStateFile(ctx, database)
	var s state
	if err != nil || json.Unmarshal(raw, &s) != nil || !s.configured() {
		return R2Config{}, errors.New("no saved R2 credentials, supply --r2-config")
	}
	return s.R2Config, nil
}
