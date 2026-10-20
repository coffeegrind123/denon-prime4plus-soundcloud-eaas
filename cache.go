package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

const (
	defaultCacheMaxBytes    int64 = 512 << 20
	minimumCacheFreeBytes   int64 = 1 << 30
	maximumCacheQuotaBytes  int64 = 2 << 30
	minimumDownloadHeadroom int64 = 1
)

var errCacheLimit = errors.New("cache limit exceeded")

type cacheEntry struct {
	path    string
	size    int64
	modTime int64
}

type cacheManager struct {
	dir          string
	maxBytes     int64
	minFreeBytes int64
	mu           sync.Mutex
	downloadSem  chan struct{}
}

func newCacheManager(dir string, maxBytes, minFreeBytes int64) (*cacheManager, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("cache max bytes must be positive")
	}
	if maxBytes > maximumCacheQuotaBytes {
		return nil, fmt.Errorf("cache max bytes %d exceeds hard safety limit %d", maxBytes, maximumCacheQuotaBytes)
	}
	if minFreeBytes < 0 {
		return nil, fmt.Errorf("cache min free bytes cannot be negative")
	}
	if minFreeBytes > math.MaxInt64/2 {
		return nil, fmt.Errorf("cache min free bytes is too large")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	manager := &cacheManager{
		dir:          dir,
		maxBytes:     maxBytes,
		minFreeBytes: minFreeBytes,
	}
	if err := manager.removePartialFiles(); err != nil {
		return nil, err
	}
	if err := manager.ensure(0); err != nil {
		return nil, err
	}
	return manager, nil
}

func (c *cacheManager) removePartialFiles() error {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".part") && isOwnedCacheName(entry.Name()) {
			if err := os.Remove(filepath.Join(c.dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func (c *cacheManager) ensure(extra int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ensureLocked(extra)
}

func (c *cacheManager) ensureLocked(extra int64) error {
	if extra < 0 {
		return fmt.Errorf("cache extra bytes cannot be negative")
	}
	entries, usage, err := c.entriesLocked(true)
	if err != nil {
		return err
	}
	completed := make([]cacheEntry, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasSuffix(entry.path, ".part") {
			completed = append(completed, entry)
		}
	}
	sort.Slice(completed, func(i, j int) bool {
		return completed[i].modTime < completed[j].modTime
	})

	available, err := freeBytes(c.dir)
	if err != nil {
		return err
	}
	for _, entry := range completed {
		if usage+extra <= c.maxBytes && available >= c.minFreeBytes {
			break
		}
		if err := os.Remove(entry.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		usage -= entry.size
		available += entry.size
	}

	if exceeds(usage, extra, c.maxBytes) {
		return fmt.Errorf("%w: usage=%d extra=%d max=%d", errCacheLimit, usage, extra, c.maxBytes)
	}
	if available < c.minFreeBytes {
		return fmt.Errorf("%w: filesystem free=%d minimum=%d", errCacheLimit, available, c.minFreeBytes)
	}
	return nil
}

func (c *cacheManager) beginDownload(ctx context.Context) error {
	sem := c.downloadSemaphore()
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-sem
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureLocked(minimumDownloadHeadroom); err != nil {
		<-sem
		return err
	}
	return nil
}

func (c *cacheManager) remainingBudget() (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, usage, err := c.entriesLocked(true)
	if err != nil {
		return 0, err
	}
	available, err := freeBytes(c.dir)
	if err != nil {
		return 0, err
	}

	budget := c.maxBytes - usage
	if freeBudget := available - c.minFreeBytes; freeBudget < budget {
		budget = freeBudget
	}
	if budget <= 0 {
		return 0, fmt.Errorf("%w: no safe cache space remains", errCacheLimit)
	}
	return budget, nil
}

func (c *cacheManager) endDownload() {
	<-c.downloadSemaphore()
}

func (c *cacheManager) downloadSemaphore() chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.downloadSem == nil {
		c.downloadSem = make(chan struct{}, 1)
	}
	return c.downloadSem
}

func (c *cacheManager) entriesLocked(includeParts bool) ([]cacheEntry, int64, error) {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return nil, 0, err
	}
	out := make([]cacheEntry, 0, len(entries))
	var usage int64
	for _, entry := range entries {
		if entry.IsDir() || !isOwnedCacheName(entry.Name()) {
			continue
		}
		if !includeParts && strings.HasSuffix(entry.Name(), ".part") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, 0, err
		}
		if info.Size() < 0 || usage > math.MaxInt64-info.Size() {
			return nil, 0, fmt.Errorf("cache usage overflow")
		}
		usage += info.Size()
		out = append(out, cacheEntry{
			path:    filepath.Join(c.dir, entry.Name()),
			size:    info.Size(),
			modTime: info.ModTime().UnixNano(),
		})
	}
	return out, usage, nil
}

func exceeds(usage, extra, limit int64) bool {
	return usage > limit || extra > limit-usage
}

func isOwnedCacheName(name string) bool {
	name = strings.TrimSuffix(name, ".part")
	extension := filepath.Ext(name)
	switch extension {
	case ".m4a", ".mp3", ".mp4", ".aac":
	default:
		return false
	}
	id := strings.TrimSuffix(name, extension)
	return isNumericTrackID(id)
}

func isNumericTrackID(id string) bool {
	if id == "" {
		return false
	}
	_, err := strconv.ParseUint(id, 10, 64)
	return err == nil
}

func freeBytes(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}
