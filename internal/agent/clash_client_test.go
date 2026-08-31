package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestClashClientDiscoversOnlySelectorsAndSendsBearer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/proxies" || r.Header.Get("Authorization") != "Bearer clash-secret" {
			t.Fatalf("request method=%s path=%s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"proxies":{"auto":{"type":"URLTest","name":"auto","now":"a","all":["a"]},"select":{"type":"Selector","name":"select","now":"b","all":["a","b"]}}}`))
	}))
	defer server.Close()

	selectors, err := (clashClient{endpoint: server.URL, secret: "clash-secret", client: server.Client()}).discover(context.Background())
	if err != nil || len(selectors) != 1 || selectors[0].Name != "select" || selectors[0].Current != "b" || len(selectors[0].Choices) != 2 {
		t.Fatalf("selectors=%+v err=%v", selectors, err)
	}
}

func TestClashClientSwitchesWithReadBackAndIdempotency(t *testing.T) {
	var mu sync.Mutex
	current := "a"
	puts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer clash-secret" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"proxies":{"select":{"type":"Selector","name":"select","now":"` + current + `","all":["a","b"]}}}`))
		case http.MethodPut:
			if r.URL.EscapedPath() != "/proxies/select" {
				t.Fatalf("path=%q", r.URL.EscapedPath())
			}
			var body struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name != "b" {
				t.Fatalf("body=%+v err=%v", body, err)
			}
			puts++
			current = body.Name
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("method=%s", r.Method)
		}
	}))
	defer server.Close()
	client := clashClient{endpoint: server.URL, secret: "clash-secret", client: server.Client()}
	result, err := client.switchSelector(context.Background(), "select", "b")
	if err != nil || !result.Changed || result.Current != "b" || puts != 1 {
		t.Fatalf("result=%+v puts=%d err=%v", result, puts, err)
	}
	result, err = client.switchSelector(context.Background(), "select", "b")
	if err != nil || result.Changed || result.Current != "b" || puts != 1 {
		t.Fatalf("idempotent result=%+v puts=%d err=%v", result, puts, err)
	}
}

func TestClashClientRejectsStaleAndUnverifiedSwitches(t *testing.T) {
	tests := []struct {
		name, selector, choice, after string
		wantCategory                  string
		wantPuts                      int
		putStatus                     int
	}{
		{name: "selector missing", selector: "missing", choice: "b", wantCategory: "selector_not_found"},
		{name: "choice missing", selector: "select", choice: "missing", wantCategory: "choice_not_found"},
		{name: "verification mismatch", selector: "select", choice: "b", after: "a", wantCategory: "switch_verification_failed", wantPuts: 1},
		{name: "PUT failure", selector: "select", choice: "b", wantCategory: "switch_failed", wantPuts: 1, putStatus: http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gets, puts := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					puts++
					if test.putStatus != 0 {
						w.WriteHeader(test.putStatus)
						return
					}
					w.WriteHeader(http.StatusNoContent)
					return
				}
				gets++
				current := "a"
				if gets > 1 && test.after != "" {
					current = test.after
				}
				_, _ = w.Write([]byte(`{"proxies":{"select":{"type":"Selector","name":"select","now":"` + current + `","all":["a","b"]}}}`))
			}))
			_, err := (clashClient{endpoint: server.URL, client: server.Client()}).switchSelector(context.Background(), test.selector, test.choice)
			server.Close()
			var switchErr *selectorSwitchError
			if !errors.As(err, &switchErr) || switchErr.category != test.wantCategory || puts != test.wantPuts {
				t.Fatalf("error=%v category=%v puts=%d", err, switchErr, puts)
			}
		})
	}
}

func TestClashClientSwitchReportsUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	_, err := (clashClient{endpoint: server.URL, secret: "wrong", client: server.Client()}).switchSelector(context.Background(), "select", "b")
	var switchErr *selectorSwitchError
	if !errors.As(err, &switchErr) || switchErr.category != "clash_api_unauthorized" {
		t.Fatalf("error=%v category=%v", err, switchErr)
	}
}

func TestClashClientRejectsFailuresAndMalformedSelectors(t *testing.T) {
	tests := []struct {
		status int
		body   string
	}{
		{http.StatusNotFound, `{}`},
		{http.StatusOK, `{`},
		{http.StatusOK, `{}`},
		{http.StatusOK, `{"proxies":{"s":{"type":"Selector","name":"s","now":"missing","all":["a"]}}}`},
	}
	for _, test := range tests {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(test.status)
			_, _ = w.Write([]byte(test.body))
		}))
		_, err := (clashClient{endpoint: server.URL, client: server.Client()}).discover(context.Background())
		server.Close()
		if err == nil {
			t.Fatalf("status=%d body=%q unexpectedly accepted", test.status, test.body)
		}
	}
}

func TestClashSecretIsNotSerialized(t *testing.T) {
	config := Config{ClashAPISecret: "do-not-leak"}
	body, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(body) || bytes.Contains(body, []byte("do-not-leak")) {
		t.Fatal("secret leaked from serialized config")
	}
}
