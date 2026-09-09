package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWebDomainPolicyLifecycleAndPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "policy.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := store.GetWebDomainPolicy(ctx)
	if err != nil || policy.Mode != WebDomainModeExact || len(policy.Suffixes) != 0 {
		t.Fatalf("initial policy=%+v err=%v", policy, err)
	}
	changed, policy, err := store.AddWebDomainSuffix(ctx, "navolyn.com", time.Unix(10, 0))
	if err != nil || !changed || policy.Mode != WebDomainModeSuffix || len(policy.Suffixes) != 1 {
		t.Fatalf("first add changed=%v policy=%+v err=%v", changed, policy, err)
	}
	changed, policy, err = store.AddWebDomainSuffix(ctx, "navolyn.com", time.Unix(11, 0))
	if err != nil || changed || len(policy.Suffixes) != 1 {
		t.Fatalf("duplicate add changed=%v policy=%+v err=%v", changed, policy, err)
	}
	changed, policy, err = store.AddWebDomainSuffix(ctx, "example.com", time.Unix(12, 0))
	if err != nil || !changed || len(policy.Suffixes) != 2 {
		t.Fatalf("second add changed=%v policy=%+v err=%v", changed, policy, err)
	}
	changed, policy, err = store.RemoveWebDomainSuffix(ctx, "missing.example", time.Unix(13, 0))
	if err != nil || changed || len(policy.Suffixes) != 2 {
		t.Fatalf("missing remove changed=%v policy=%+v err=%v", changed, policy, err)
	}
	changed, policy, err = store.RemoveWebDomainSuffix(ctx, "example.com", time.Unix(14, 0))
	if err != nil || !changed || len(policy.Suffixes) != 1 {
		t.Fatalf("remove changed=%v policy=%+v err=%v", changed, policy, err)
	}
	if _, _, err := store.RemoveWebDomainSuffix(ctx, "navolyn.com", time.Unix(15, 0)); !errors.Is(err, ErrLastWebDomainSuffix) {
		t.Fatalf("last remove err=%v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	policy, err = store.GetWebDomainPolicy(ctx)
	if err != nil || policy.Mode != WebDomainModeSuffix || len(policy.Suffixes) != 1 || policy.Suffixes[0].Suffix != "navolyn.com" {
		t.Fatalf("persisted policy=%+v err=%v", policy, err)
	}
	changed, policy, err = store.DisableWebDomainSuffixes(ctx, time.Unix(16, 0))
	if err != nil || !changed || policy.Mode != WebDomainModeExact || len(policy.Suffixes) != 0 {
		t.Fatalf("disabled policy=%+v err=%v", policy, err)
	}
	changed, policy, err = store.DisableWebDomainSuffixes(ctx, time.Unix(17, 0))
	if err != nil || changed || policy.Revision != 4 {
		t.Fatalf("duplicate disable changed=%v policy=%+v err=%v", changed, policy, err)
	}
}

func TestWebDomainConcurrentAddsRemainAtomic(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "concurrent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 8)
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, _, err := store.AddWebDomainSuffix(context.Background(), fmt.Sprintf("example%d.com", index), time.Now())
			errorsSeen <- err
		}(index)
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	policy, err := store.GetWebDomainPolicy(context.Background())
	if err != nil || policy.Mode != WebDomainModeSuffix || len(policy.Suffixes) != 8 {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}
}
