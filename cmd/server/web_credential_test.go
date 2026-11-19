package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"404-probe/internal/auth"
	appserver "404-probe/internal/server"
	"404-probe/internal/storage"
)

func TestWebCredentialPasswordChangeReconciliation(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		established bool
		replaceFile bool
		adopt       bool
	}{
		{name: "unadopted_file_from_revision_zero", replaceFile: true},
		{name: "unadopted_file_with_credential", established: true, replaceFile: true},
		{name: "adopt_then_web_change", replaceFile: true, adopt: true},
		{name: "unchanged_file_from_revision_zero"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			database := filepath.Join(dir, "server.db")
			hashFile := filepath.Join(dir, "password.hash")
			store, err := openStore(database)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			writeFile := func(password string) storage.WebCredential {
				t.Helper()
				hash, err := auth.HashPassword([]byte(password))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(hashFile, 0o600); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if err := os.WriteFile(hashFile, []byte(hash+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(hashFile, 0o400); err != nil {
					t.Fatal(err)
				}
				file, err := readWebPasswordFile(hashFile)
				if err != nil {
					t.Fatal(err)
				}
				return file
			}
			const original = "original installer password"
			const rootPassword = "root reset password value"
			const webPassword = "web chosen new password"
			initial := writeFile(original)
			if scenario.established {
				if err := store.AdoptWebCredential(ctx, initial.PasswordHash, initial.FileSHA256, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			startup, err := reconcileWebPasswordFile(ctx, store, hashFile, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			app, err := appserver.NewApp(store, 30*time.Second, nil, appserver.WithWebAuthentication(appserver.WebAuthenticationConfig{
				PasswordHash: startup.PasswordHash, PublicOrigin: "https://probe.test",
				PasswordFileSHA256: startup.FileSHA256,
			}))
			if err != nil {
				t.Fatal(err)
			}
			defer app.Shutdown()
			file := initial
			current := original
			if scenario.replaceFile {
				file = writeFile(rootPassword)
			}
			if scenario.adopt {
				if err := run([]string{"web-credential", "adopt", "--db", database, "--hash-file", hashFile}); err != nil {
					t.Fatal(err)
				}
				current = rootPassword
			}
			cookie := credentialWebLogin(t, app.Handler(), current)
			request := httptest.NewRequest(http.MethodGet, "/api/v1/web/session", nil)
			request.Host = "probe.test"
			request.AddCookie(cookie)
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)
			var session struct {
				CSRF string `json:"csrf_token"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &session); err != nil || response.Code != http.StatusOK || session.CSRF == "" {
				t.Fatalf("session status=%d err=%v", response.Code, err)
			}
			body, err := json.Marshal(map[string]string{"current_password": current, "new_password": webPassword, "confirm_password": webPassword})
			if err != nil {
				t.Fatal(err)
			}
			request = httptest.NewRequest(http.MethodPost, "/api/v1/web/settings/password", strings.NewReader(string(body)))
			request.Host = "probe.test"
			request.Header.Set("Origin", "https://probe.test")
			request.Header.Set("Sec-Fetch-Site", "same-origin")
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-CSRF-Token", session.CSRF)
			request.AddCookie(cookie)
			response = httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusNoContent {
				t.Fatalf("Web password change status=%d", response.Code)
			}
			before, err := store.WebCredential(ctx)
			if err != nil || !auth.VerifyPassword(before.PasswordHash, []byte(webPassword)) {
				t.Fatalf("Web password was not stored: %v", err)
			}
			if _, err := reconcileWebPasswordFile(ctx, store, hashFile, time.Now()); err != nil {
				t.Fatal(err)
			}
			after, err := store.WebCredential(ctx)
			if err != nil {
				t.Fatal(err)
			}
			wantPassword, wantRevision := webPassword, before.Revision
			if scenario.replaceFile && !scenario.adopt {
				wantPassword, wantRevision = rootPassword, before.Revision+1
			}
			if after.Revision != wantRevision || after.FileSHA256 != file.FileSHA256 || !auth.VerifyPassword(after.PasswordHash, []byte(wantPassword)) {
				t.Fatalf("reconciliation revision=%d, want %d; expected password retained=%v, file digest matches=%v",
					after.Revision, wantRevision, auth.VerifyPassword(after.PasswordHash, []byte(wantPassword)), after.FileSHA256 == file.FileSHA256)
			}
		})
	}
}

func credentialWebLogin(t *testing.T, handler http.Handler, password string) *http.Cookie {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/login", nil)
	request.Host = "probe.test"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	proof := regexp.MustCompile(`name="csrf_token" value="([A-Za-z0-9_-]+)"`).FindStringSubmatch(response.Body.String())
	if response.Code != http.StatusOK || len(proof) != 2 {
		t.Fatalf("login form status=%d, proof count=%d", response.Code, len(proof))
	}
	form := url.Values{"csrf_token": {proof[1]}, "password": {password}}
	request = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	request.Host = "probe.test"
	request.Header.Set("Origin", "https://probe.test")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range response.Result().Cookies() {
		request.AddCookie(cookie)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("login status=%d", response.Code)
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "__Host-404-probe-session" && cookie.Value != "" {
			return cookie
		}
	}
	t.Fatal("login did not issue a session cookie")
	return nil
}

func TestWebCredentialStartupAndAdopt(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	database := filepath.Join(dir, "server.db")
	hashFile := filepath.Join(dir, "web-password.hash")
	store, err := openStore(database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(100, 0)
	writeHash := func(password, ending string) storage.WebCredential {
		t.Helper()
		hash, err := auth.HashPassword([]byte(password))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(hashFile, []byte(hash+ending), 0o400); err != nil {
			t.Fatal(err)
		}
		file, err := readWebPasswordFile(hashFile)
		if err != nil {
			t.Fatal(err)
		}
		return file
	}
	initial := writeHash("initial fixture password", "\n")
	if _, err := reconcileWebPasswordFile(ctx, store, hashFile, now); err != nil {
		t.Fatal(err)
	}
	if got, err := store.WebCredential(ctx); err != nil || got.Revision != 0 {
		t.Fatal("startup persisted the initial file credential", err)
	}
	webHash, err := auth.HashPassword([]byte("web fixture password"))
	if err != nil {
		t.Fatal(err)
	}
	if revision, err := store.ReplaceWebCredential(ctx, 0, webHash, initial.FileSHA256, now); err != nil || revision != 1 {
		t.Fatalf("web revision=%d err=%v", revision, err)
	}
	if _, err := reconcileWebPasswordFile(ctx, store, hashFile, now); err != nil {
		t.Fatal(err)
	}
	if got, err := store.WebCredential(ctx); err != nil || got.Revision != 1 || got.PasswordHash != webHash {
		t.Fatal("same password file replaced the database credential", err)
	}
	if err := os.Remove(hashFile); err != nil {
		if err := os.Chmod(hashFile, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(hashFile); err != nil {
			t.Fatal(err)
		}
	}
	replaced := writeHash("root fixture password", "\r\n")
	if _, err := reconcileWebPasswordFile(ctx, store, hashFile, now); err != nil {
		t.Fatal(err)
	}
	if got, err := store.WebCredential(ctx); err != nil || got.Revision != 2 || got.PasswordHash != replaced.PasswordHash || got.FileSHA256 != replaced.FileSHA256 {
		t.Fatal("startup did not adopt the replaced password file", err)
	}
	for range 2 {
		if err := run([]string{"web-credential", "adopt", "--db", database, "--hash-file", hashFile}); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := store.WebCredential(ctx); err != nil || got.Revision != 4 || got.FileSHA256 != replaced.FileSHA256 {
		t.Fatal("adopt failed to advance the revision", err)
	}
}
