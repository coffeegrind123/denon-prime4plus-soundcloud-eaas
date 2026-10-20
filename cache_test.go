package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCacheEvictsOldestCompletedFiles(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "100.m4a")
	newPath := filepath.Join(dir, "200.mp3")
	writeCacheFixture(t, oldPath, 6, time.Now().Add(-time.Hour))
	writeCacheFixture(t, newPath, 6, time.Now())

	manager := &cacheManager{dir: dir, maxBytes: 10}
	if err := manager.ensure(5); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	for _, path := range []string{oldPath, newPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s was not evicted", path)
		}
	}
}

func TestCachePreservesUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	unrelated := filepath.Join(dir, "notes.txt")
	writeCacheFixture(t, unrelated, 4096, time.Now())

	manager := &cacheManager{dir: dir, maxBytes: 1}
	if err := manager.ensure(1); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated file removed: %v", err)
	}
}

func TestCacheRemovesOnlyOwnedPartialFiles(t *testing.T) {
	dir := t.TempDir()
	ownedPart := filepath.Join(dir, "123.m4a.part")
	unrelatedPart := filepath.Join(dir, "cover.part")
	writeCacheFixture(t, ownedPart, 1, time.Now())
	writeCacheFixture(t, unrelatedPart, 1, time.Now())

	if _, err := newCacheManager(dir, defaultCacheMaxBytes, 0); err != nil {
		t.Fatalf("new cache manager: %v", err)
	}
	if _, err := os.Stat(ownedPart); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned partial file was not removed")
	}
	if _, err := os.Stat(unrelatedPart); err != nil {
		t.Fatalf("unrelated partial file removed: %v", err)
	}
}

func TestCacheRejectsImpossibleQuota(t *testing.T) {
	manager := &cacheManager{dir: t.TempDir(), maxBytes: 10}
	err := manager.ensure(11)
	if !errors.Is(err, errCacheLimit) {
		t.Fatalf("error = %v", err)
	}
}

func TestCacheRejectsFreeSpaceFloorBreach(t *testing.T) {
	dir := t.TempDir()
	available, err := freeBytes(dir)
	if err != nil {
		t.Fatalf("free bytes: %v", err)
	}
	manager := &cacheManager{
		dir:          dir,
		maxBytes:     defaultCacheMaxBytes,
		minFreeBytes: available + 1,
	}
	if err := manager.ensure(0); !errors.Is(err, errCacheLimit) {
		t.Fatalf("error = %v", err)
	}
}

func TestDownloadBudgetAccountsForCachedData(t *testing.T) {
	dir := t.TempDir()
	writeCacheFixture(t, filepath.Join(dir, "100.m4a"), 100, time.Now())
	manager := &cacheManager{dir: dir, maxBytes: 1000}

	if err := manager.beginDownload(context.Background()); err != nil {
		t.Fatalf("begin download: %v", err)
	}
	defer manager.endDownload()
	budget, err := manager.remainingBudget()
	if err != nil {
		t.Fatalf("remaining budget: %v", err)
	}
	if budget != 900 {
		t.Fatalf("budget = %d, want 900", budget)
	}
}

func TestDownloadCopyHonorsHardLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 128))
	}))
	defer server.Close()

	source := &source{
		cache:      &cacheManager{dir: t.TempDir(), maxBytes: defaultCacheMaxBytes},
		httpClient: server.Client(),
	}
	written, err := source.copyURLLimited(context.Background(), io.Discard, server.URL, 16)
	if !errors.Is(err, errCacheLimit) {
		t.Fatalf("error = %v", err)
	}
	if written != 17 {
		t.Fatalf("written = %d, want 17", written)
	}
}

func TestDownloadEvictsCompletedFilesForResponseLength(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "100.m4a")
	writeCacheFixture(t, oldPath, 900, time.Now().Add(-time.Hour))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 200))
	}))
	defer server.Close()

	source := &source{
		cache:      &cacheManager{dir: dir, maxBytes: 1000},
		httpClient: server.Client(),
	}
	if err := source.cache.beginDownload(context.Background()); err != nil {
		t.Fatalf("begin download: %v", err)
	}
	defer source.cache.endDownload()

	written, err := source.copyURLLimited(context.Background(), io.Discard, server.URL, 1000)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if written != 200 {
		t.Fatalf("written = %d, want 200", written)
	}
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("completed file was not evicted")
	}
}

func writeCacheFixture(t *testing.T, path string, size int, modTime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}
