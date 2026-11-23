package backup

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"404-probe/internal/storage"
	"filippo.io/age"
)

const fakeAccess, fakeSecret = "synthetic-access-id", "synthetic-r2-secret"

type fakeObject struct {
	body   []byte
	digest string
}

func (o fakeObject) etag() string { return `"` + sha256Hex(o.body) + `"` }

type fakeR2 struct {
	mu        sync.Mutex
	objects   map[string]fakeObject
	transient int
	uploads   int
	beforePut func()
}

func (s *fakeR2) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.Header.Get("Authorization"), "Credential="+fakeAccess+"/") {
		http.Error(w, "untrusted response mentioning "+fakeSecret, 403)
		return
	}
	if r.URL.Path != "/fixtures" && !strings.HasPrefix(r.URL.Path, "/fixtures/") {
		http.Error(w, "missing bucket", 404)
		return
	}
	key, _ := strings.CutPrefix(r.URL.Path, "/fixtures/")
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case r.Method == http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil || sha256Hex(body) != r.Header.Get("X-Amz-Content-Sha256") {
			http.Error(w, "invalid payload", 400)
			return
		}
		if r.Header.Get(archiveHash) != "" {
			s.uploads++
			if s.beforePut != nil {
				s.beforePut()
			}
			if s.transient > 0 {
				s.transient--
				http.Error(w, "retry", 503)
				return
			}
		}
		if _, exists := s.objects[key]; exists && r.Header.Get("If-None-Match") == "*" {
			w.WriteHeader(412)
			return
		}
		s.objects[key] = fakeObject{body, r.Header.Get(archiveHash)}
	case r.Method == http.MethodGet && key == "/fixtures":
		var keys []string
		for name := range s.objects {
			if strings.HasPrefix(name, r.URL.Query().Get("prefix")) {
				keys = append(keys, name)
			}
		}
		slices.Sort(keys)
		start, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Query().Get("continuation-token"), "+="))
		end := min(start+3, len(keys))
		page := struct {
			XMLName               xml.Name `xml:"ListBucketResult"`
			Contents              []listedObject
			IsTruncated           bool
			NextContinuationToken string
		}{IsTruncated: end < len(keys)}
		if page.IsTruncated {
			page.NextContinuationToken = "+=" + strconv.Itoa(end)
		}
		for _, name := range keys[start:end] {
			page.Contents = append(page.Contents, listedObject{Key: name, ETag: s.objects[name].etag()})
		}
		_ = xml.NewEncoder(w).Encode(page)
	case r.Method == http.MethodDelete:
		if obj := s.objects[key]; r.Header.Get("If-Match") != "" && r.Header.Get("If-Match") != obj.etag() {
			w.WriteHeader(412)
			return
		}
		delete(s.objects, key)
		w.WriteHeader(204)
	case r.Method == http.MethodHead || r.Method == http.MethodGet:
		obj, ok := s.objects[key]
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Header().Set(archiveHash, obj.digest)
		w.Header().Set("ETag", obj.etag())
		w.Header().Set("Content-Length", strconv.Itoa(len(obj.body)))
		if r.Method == http.MethodGet {
			_, _ = w.Write(obj.body)
		}
	default:
		w.WriteHeader(405)
	}
}

func TestManagerPrunesOnlyExpiredBackupWork(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	dbPath := filepath.Join(stateDir, "live.db")
	store, err := storage.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Now()
	old := now.Add(-backupWorkMaxAge - time.Hour)
	stale := filepath.Join(stateDir, backupWorkPrefix+"1")
	recent := filepath.Join(stateDir, backupWorkPrefix+"2")
	unrelated := filepath.Join(stateDir, "other-service-data")
	manual := filepath.Join(stateDir, backupWorkPrefix+"manual")
	for _, dir := range []string{stale, recent, unrelated, manual} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "snapshot"), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	looseFile := filepath.Join(stateDir, backupWorkPrefix+"3")
	if err := os.WriteFile(looseFile, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	link := filepath.Join(stateDir, backupWorkPrefix+"4")
	keep := []string{dbPath, recent, unrelated, manual, looseFile, external}
	if err := os.Symlink(external, link); err == nil {
		keep = append(keep, link)
	} else if runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	for _, path := range []string{stale, unrelated, manual, looseFile, external} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := NewManager(ctx, store, nil, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired work directory should be absent: %v", err)
	}
	for _, path := range keep {
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("expected %s to survive startup: %v", path, err)
		}
	}
}

func TestOffsiteBackupScheduleRestoreAndFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	work := t.TempDir()
	dbPath := filepath.Join(work, "live.db")
	store, err := storage.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sqlite, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout%285000%29")
	if err != nil {
		t.Fatal(err)
	}
	defer sqlite.Close()
	if _, err := sqlite.Exec(`CREATE TABLE snapshot_fixture(n INTEGER); INSERT INTO snapshot_fixture VALUES(0),(0)`); err != nil {
		t.Fatal(err)
	}
	env := "PROBE_404_WEB_PUBLIC_ORIGIN=https://backup-fixture.invalid\n"
	envPath := filepath.Join(work, EnvironmentName)
	if err := os.WriteFile(envPath, []byte(env), 0600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	manager, err := NewManager(ctx, store, map[string]string{EnvironmentName: envPath}, logger)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 22, 3, 59, 30, 0, time.Local)
	manager.clock = func() time.Time { return now }
	manager.retryDelay = []time.Duration{time.Millisecond, time.Millisecond}
	remote := &fakeR2{objects: map[string]fakeObject{}, transient: 2}
	server := httptest.NewServer(remote)
	defer server.Close()
	r2 := R2Config{Destination: Destination{Endpoint: server.URL, Bucket: "fixtures", Prefix: "隔离 /a+b"}, Credentials: Credentials{fakeAccess, fakeSecret}}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	edit := Edit{Settings: Settings{R2Config: r2, Enabled: true, Keep: 7, DailyAt: "04:00", Recipient: identity.Recipient().String()}}
	if _, err := manager.Save(ctx, edit); err == nil {
		t.Fatal("enabled without private-key acknowledgement")
	}
	if err := manager.Test(ctx, r2); err != nil || len(remote.objects) != 0 {
		t.Fatalf("test connection must put and delete its own object: %v", err)
	}
	edit.KeySaved = true
	view, err := manager.Save(ctx, edit)
	if err != nil || !view.Enabled || view.Next != now.Add(30*time.Second).UnixMilli() {
		t.Fatalf("enable: %+v %v", view, err)
	}
	assertNoSecrets(t, view, identity)
	client, err := newR2(r2)
	if err != nil {
		t.Fatal(err)
	}
	for n := 12; n > 0; n-- {
		name := archiveName(now.AddDate(0, 0, -n), 45)
		remote.objects[client.prefix()+name] = fakeObject{[]byte(name), sha256Hex([]byte(name))}
	}
	foreign := client.prefix() + archiveName(now.AddDate(0, 0, -30), 45)
	nested := client.prefix() + "other/" + archiveName(now, 45)
	remote.objects[foreign] = fakeObject{body: []byte("user object with a matching name")}
	remote.objects[nested] = fakeObject{[]byte("outside prefix"), strings.Repeat("a", 64)}
	var writes atomic.Int64
	writerCtx, stopWriter := context.WithCancel(ctx)
	writesDone := make(chan error, 1)
	go func() {
		for writerCtx.Err() == nil {
			if _, err := sqlite.Exec(`UPDATE snapshot_fixture SET n=n+1`); err != nil {
				writesDone <- err
				return
			}
			writes.Add(1)
			time.Sleep(time.Millisecond)
		}
		writesDone <- nil
	}()
	defer stopWriter()
	remote.beforePut = func() {
		// The HTTP upload is outstanding until this SQLite write returns.
		writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if _, err := sqlite.ExecContext(writeCtx, `UPDATE snapshot_fixture SET n=n+1`); err != nil {
			t.Errorf("write while streaming backup: %v", err)
		}
	}
	if _, err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(); !errors.Is(err, ErrBusy) {
		t.Fatal("overlapping backup accepted")
	}
	manager.run(ctx, <-manager.jobs)
	stopWriter()
	if err := <-writesDone; err != nil {
		t.Fatal(err)
	}
	if writes.Load() == 0 {
		t.Fatal("fixture did not issue background writes")
	}
	remote.beforePut = nil
	status := manager.Status()
	if status.LastSuccess == 0 || status.Alert || status.ObjectCount == nil || *status.ObjectCount != 7 || remote.uploads != 3 || len(remote.objects) != 9 {
		t.Fatalf("backup/retry/retention: %+v, puts=%d, objects=%d", status, remote.uploads, len(remote.objects))
	}
	if _, ok := remote.objects[foreign]; !ok {
		t.Fatal("retention deleted a foreign object with a matching name")
	}
	if _, ok := remote.objects[nested]; !ok {
		t.Fatal("retention deleted an object in another prefix")
	}
	version, err := store.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key := client.prefix() + archiveName(now, version)
	stream, err := Download(ctx, r2, key)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := io.ReadAll(stream)
	stream.Close()
	if err != nil {
		t.Fatal(err)
	}
	assertRestoredSnapshot(t, ctx, work, archive, identity, env)
	if matches, err := filepath.Glob(filepath.Join(work, ".offsite-backup-*")); err != nil || len(matches) != 0 {
		t.Fatalf("backup staging left behind: %v, %v", matches, err)
	}
	now = now.Add(time.Minute)
	scheduleCtx, stopSchedule := context.WithCancel(ctx)
	scheduleDone := make(chan struct{})
	go func() { defer close(scheduleDone); manager.Run(scheduleCtx) }()
	for ctx.Err() == nil && manager.Status().LastSuccess != now.UnixMilli() {
		time.Sleep(5 * time.Millisecond)
	}
	stopSchedule()
	<-scheduleDone
	if _, ok := manager.scheduled(ctx, now); ok || manager.Status().LastSuccess != now.UnixMilli() {
		t.Fatal("daily backup did not complete once")
	}
	resumed, err := NewManager(ctx, store, nil, logger)
	if err != nil || resumed.Status().LastSuccess != now.UnixMilli() {
		t.Fatal("backup state did not survive restart")
	}
	for _, failure := range []struct{ access, bucket, reason string }{
		{"wrong-key", "fixtures", "credentials"},
		{fakeAccess, "missing", "does not exist"},
		{fakeAccess, "fixtures", "network connection"},
	} {
		if failure.reason == "network connection" {
			server.Close()
		}
		edit.AccessKey, edit.Bucket = failure.access, failure.bucket
		edit.Revision = manager.Status().Revision
		if _, err := manager.Save(ctx, edit); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Start(); err != nil {
			t.Fatal(err)
		}
		manager.run(ctx, <-manager.jobs)
		status := manager.Status()
		if !status.Alert || !strings.Contains(status.Reason, failure.reason) {
			t.Fatalf("backup failure: %+v", status)
		}
		assertNoSecrets(t, status, identity)
	}
	manager.mu.Lock()
	manager.state.Failed = false
	manager.mu.Unlock()
	now = now.Add(48*time.Hour + time.Second)
	if status := manager.Status(); !status.Overdue || !status.Alert {
		t.Fatalf("overdue backup was not flagged: %+v", status)
	}
	for _, secret := range []string{fakeAccess, fakeSecret, identity.String()} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("backup logs expose a credential")
		}
	}
}

func assertRestoredSnapshot(t *testing.T, ctx context.Context, work string, archive []byte, identity *age.X25519Identity, env string) {
	t.Helper()
	dir := filepath.Join(work, "extracted")
	verification, err := Extract(ctx, bytes.NewReader(archive), identity, dir)
	if err != nil || verification.Integrity != "ok" {
		t.Fatalf("extract: %+v %v", verification, err)
	}
	if restored, err := os.ReadFile(filepath.Join(dir, EnvironmentName)); err != nil || string(restored) != env {
		t.Fatalf("Server configuration was not recovered: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var low, high int
	if err := db.QueryRow(`SELECT min(n), max(n) FROM snapshot_fixture`).Scan(&low, &high); err != nil || low != high {
		t.Fatalf("transaction was torn in snapshot: %d vs %d, %v", low, high, err)
	}
	wrong, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	damaged := bytes.Clone(archive)
	damaged[len(damaged)-1] ^= 1
	bad := filepath.Join(work, "bad")
	if _, err := Extract(ctx, bytes.NewReader(archive), wrong, bad); err == nil || !strings.Contains(err.Error(), "private key") {
		t.Fatalf("wrong private key: %v", err)
	}
	if _, err := Extract(ctx, bytes.NewReader(damaged), identity, bad); err == nil {
		t.Fatal("damaged archive accepted")
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(999,0); PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	var newer bytes.Buffer
	if err := writeArchive(&newer, identity.Recipient(), 999, map[string]string{DatabaseName: filepath.Join(dir, DatabaseName)}); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(ctx, &newer, identity, bad); !errors.Is(err, storage.ErrUnsupportedSchemaVersion) {
		t.Fatalf("newer schema: %v", err)
	}
}

func assertNoSecrets(t *testing.T, status Status, identity *age.X25519Identity) {
	t.Helper()
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{fakeAccess, fakeSecret, identity.String()} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("status exposes a credential")
		}
	}
}

func TestR2SignerAWSExample(t *testing.T) {
	request, err := http.NewRequest("PUT", "https://examplebucket.s3.amazonaws.com/test%24file.text", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Date", "Fri, 24 May 2013 00:00:00 GMT")
	request.Header.Set("X-Amz-Storage-Class", "REDUCED_REDUNDANCY")
	signR2(request, Credentials{"AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC), "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072", "us-east-1")
	if auth := request.Header.Get("Authorization"); !strings.HasSuffix(auth, "Signature=98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd") {
		t.Fatalf("AWS example signature did not match: %s", auth)
	}
}
