package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
