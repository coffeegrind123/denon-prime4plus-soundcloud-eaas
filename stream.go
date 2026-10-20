package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"git.maid.zone/stuff/soundcloak/lib/cfg"
	"git.maid.zone/stuff/soundcloak/lib/sc"
)

type resolvedStream struct {
	url      string
	protocol sc.Protocol
	ext      string
}

type hlsPlan struct {
	playlist *hlsPlaylist
	urls     []string
	sizes    []int64
	total    int64
}

const mediaMaterializeTimeout = 15 * time.Minute

func mediaMaterializeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), mediaMaterializeTimeout)
}

func (s *source) httpHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/download/", s.handleDownload)
	return mux
}

func (s *source) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	requested := strings.TrimPrefix(r.URL.Path, "/download/")
	requested, err := url.PathUnescape(requested)
	if err != nil {
		http.Error(w, "bad download path", http.StatusBadRequest)
		return
	}
	requested = strings.TrimSpace(strings.Trim(requested, "<>"))
	requested = strings.TrimPrefix(requested, "C:")
	requested = strings.TrimPrefix(requested, "/")
	requested = strings.ReplaceAll(requested, "\\", "/")
	base := path.Base(requested)
	id := strings.TrimSuffix(base, path.Ext(base))
	if !isNumericTrackID(id) {
		http.Error(w, "missing track id", http.StatusBadRequest)
		return
	}
	log.Printf("download track=%s range=%q", id, r.Header.Get("Range"))

	stream, err := s.resolveStream(id)
	if err != nil {
		log.Printf("resolve stream %s: %v", id, err)
		http.Error(w, "stream unavailable", http.StatusBadGateway)
		return
	}

	if stream.protocol == sc.ProtocolProgressive {
		file, err := s.materializeProgressive(r.Context(), id, stream)
		if err != nil {
			log.Printf("materialize progressive %s: %v", id, err)
			http.Error(w, "stream unavailable", http.StatusBadGateway)
			return
		}
		serveCachedMedia(w, r, file)
		return
	}

	file, err := s.materializeHLS(r.Context(), id, stream, nil)
	if err != nil {
		log.Printf("materialize HLS %s: %v", id, err)
		http.Error(w, "stream unavailable", http.StatusBadGateway)
		return
	}
	serveCachedMedia(w, r, file)
}

func (s *source) prepareTrack(ctx context.Context, id string) (uint32, string, error) {
	stream, err := s.resolveStream(id)
	if err != nil {
		return 0, "", err
	}
	extension := stream.ext
	if extension == "" {
		if stream.protocol == sc.ProtocolProgressive {
			extension = "mp3"
		} else {
			extension = "m4a"
		}
	}

	destination := filepath.Join(s.cache.dir, id+"."+extension)
	if info, ok, err := s.cachedMediaInfo(destination); ok || err != nil {
		if err != nil {
			return 0, "", err
		}
		size, err := mediaSize(info.Size())
		return size, extension, err
	}

	var size int64
	var plan *hlsPlan
	if stream.protocol == sc.ProtocolProgressive {
		size, err = s.remoteSize(ctx, stream.url)
	} else {
		plan, err = s.planHLS(ctx, stream.url)
		if err == nil {
			size = plan.total
		}
	}
	if err == nil {
		s.startPrefetch(id, stream, plan)
		size32, err := mediaSize(size)
		return size32, extension, err
	}

	file, materializeErr := s.materialize(ctx, id, stream, plan)
	if materializeErr != nil {
		return 0, "", materializeErr
	}
	defer file.Close()
	info, statErr := file.Stat()
	if statErr != nil {
		return 0, "", statErr
	}
	size32, err := mediaSize(info.Size())
	return size32, extension, err
}

func (s *source) materialize(ctx context.Context, id string, stream resolvedStream, plan *hlsPlan) (*os.File, error) {
	if stream.protocol == sc.ProtocolProgressive {
		return s.materializeProgressive(ctx, id, stream)
	}
	return s.materializeHLS(ctx, id, stream, plan)
}

func (s *source) cachedMediaInfo(path string) (os.FileInfo, bool, error) {
	file, ok, err := s.openCachedMedia(path)
	if err != nil || !ok {
		return nil, ok, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	return info, true, nil
}

func mediaSize(size int64) (uint32, error) {
	if size <= 0 || size > int64(^uint32(0)) {
		return 0, fmt.Errorf("invalid cached media size %d", size)
	}
	return uint32(size), nil
}

func (s *source) startPrefetch(id string, stream resolvedStream, plan *hlsPlan) {
	s.mu.Lock()
	if s.prefetching == nil {
		s.prefetching = map[string]bool{}
	}
	if s.prefetching[id] {
		s.mu.Unlock()
		return
	}
	s.prefetching[id] = true
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.prefetching, id)
			s.mu.Unlock()
		}()
		file, err := s.materialize(context.Background(), id, stream, plan)
		if err != nil {
			log.Printf("prefetch %s: %v", id, err)
			return
		}
		_ = file.Close()
	}()
}

func (s *source) materializeProgressive(ctx context.Context, id string, stream resolvedStream) (*os.File, error) {
	lock := s.streamLock(id)
	lock.Lock()
	defer lock.Unlock()

	extension := stream.ext
	if extension == "" {
		extension = "mp3"
	}
	destination := filepath.Join(s.cache.dir, id+"."+extension)

	if file, ok, err := s.openCachedMedia(destination); ok || err != nil {
		return file, err
	}
	if err := s.cache.beginDownload(ctx); err != nil {
		return nil, err
	}
	defer s.cache.endDownload()

	if file, ok, err := s.openCachedMedia(destination); ok || err != nil {
		return file, err
	}

	ctx, cancel := mediaMaterializeContext(ctx)
	defer cancel()
	temporary := destination + ".part"
	output, err := os.Create(temporary)
	if err != nil {
		return nil, err
	}
	if _, err := s.copyURLLimited(ctx, output, stream.url, s.cache.maxBytes); err != nil {
		_ = output.Close()
		_ = os.Remove(temporary)
		return nil, err
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		_ = os.Remove(temporary)
		return nil, err
	}
	if err := output.Close(); err != nil {
		_ = os.Remove(temporary)
		return nil, err
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)
		return nil, err
	}
	log.Printf("cached progressive track %s (%s)", id, destination)
	file, ok, err := s.openCachedMedia(destination)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("cached progressive file disappeared")
	}
	return file, nil
}

func (s *source) resolveStream(id string) (resolvedStream, error) {
	track, err := s.track(id)
	if err != nil {
		return resolvedStream{}, err
	}

	selected := track.Media.SelectCompatibleRestream(cfg.AudioAAC)
	if selected == nil {
		return resolvedStream{}, fmt.Errorf("no public non-DRM stream")
	}
	if selected.HasDRM() {
		return resolvedStream{}, fmt.Errorf("selected stream requires DRM")
	}
	log.Printf("stream %s selected protocol=%s preset=%q legacy=%t",
		id, selected.Format.Protocol, selected.Preset, selected.Legacy)

	signed, err := selected.GetUncachedStream("", track)
	if err != nil {
		return resolvedStream{}, err
	}
	return resolvedStream{
		url:      signed.URL,
		protocol: selected.Format.Protocol,
		ext:      selected.ToExt(),
	}, nil
}

func (s *source) loadHLSPlaylist(ctx context.Context, target string) (*hlsPlaylist, error) {
	data, err := s.fetch(ctx, target)
	if err != nil {
		return nil, err
	}
	playlist := newHLSPlaylist(target)
	if err := playlist.parse(data); err != nil {
		return nil, err
	}
	return playlist, nil
}

func (s *source) remoteSize(ctx context.Context, target string) (int64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
	if err != nil {
		return 0, err
	}
	response, err := s.httpClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("HEAD %s: status %d", target, response.StatusCode)
	}
	if response.ContentLength <= 0 {
		return 0, fmt.Errorf("HEAD %s: missing content length", target)
	}
	return response.ContentLength, nil
}

func (s *source) hlsSize(ctx context.Context, target string) (int64, error) {
	plan, err := s.planHLS(ctx, target)
	if err != nil {
		return 0, err
	}
	return plan.total, nil
}

func (s *source) planHLS(ctx context.Context, target string) (*hlsPlan, error) {
	playlist, err := s.loadHLSPlaylist(ctx, target)
	if err != nil {
		return nil, err
	}
	urls := make([]string, 0, len(playlist.segments)+1)
	if playlist.initialization != "" {
		urls = append(urls, playlist.initialization)
	}
	urls = append(urls, playlist.segments...)
	if len(urls) == 0 {
		return nil, fmt.Errorf("HLS playlist has no media")
	}

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	workers := min(16, len(urls))
	jobs := make(chan int)
	sizes := make([]int64, len(urls))
	var wait sync.WaitGroup
	var firstErr error
	var errOnce sync.Once
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := range jobs {
				size, err := s.remoteSize(workerCtx, urls[index])
				if err != nil {
					errOnce.Do(func() {
						firstErr = err
						cancel()
					})
					continue
				}
				sizes[index] = size
			}
		}()
	}
sending:
	for index := range urls {
		select {
		case jobs <- index:
		case <-workerCtx.Done():
			break sending
		}
	}
	close(jobs)
	wait.Wait()
	if firstErr != nil {
		return nil, firstErr
	}

	var total int64
	for _, size := range sizes {
		if size <= 0 || total > int64(^uint32(0))-size {
			return nil, fmt.Errorf("invalid HLS media size")
		}
		total += size
	}
	return &hlsPlan{
		playlist: playlist,
		urls:     urls,
		sizes:    sizes,
		total:    total,
	}, nil
}

func (s *source) materializeHLS(ctx context.Context, id string, stream resolvedStream, plan *hlsPlan) (*os.File, error) {
	lock := s.streamLock(id)
	lock.Lock()
	defer lock.Unlock()

	extension := stream.ext
	if extension == "" {
		extension = "m4a"
	}
	destination := filepath.Join(s.cache.dir, id+"."+extension)

	if file, ok, err := s.openCachedMedia(destination); ok || err != nil {
		return file, err
	}
	if err := s.cache.beginDownload(ctx); err != nil {
		return nil, err
	}
	defer s.cache.endDownload()

	if file, ok, err := s.openCachedMedia(destination); ok || err != nil {
		return file, err
	}

	ctx, cancel := mediaMaterializeContext(ctx)
	defer cancel()

	if plan == nil {
		loadedPlan, loadErr := s.planHLS(ctx, stream.url)
		if loadErr != nil {
			return nil, loadErr
		}
		plan = loadedPlan
	}
	if err := s.cache.ensure(plan.total); err != nil {
		return nil, err
	}

	temporary := destination + ".part"
	output, err := os.Create(temporary)
	if err != nil {
		return nil, err
	}
	defer output.Close()
	var written int64

	const (
		maxBatchItems = 8
		maxBatchBytes = 32 << 20
	)
	for start := 0; start < len(plan.urls); {
		end := start
		var batchBytes int64
		for end < len(plan.urls) && end-start < maxBatchItems {
			size := plan.sizes[end]
			if size > maxBatchBytes {
				_ = os.Remove(temporary)
				return nil, fmt.Errorf("HLS segment is too large: %d", size)
			}
			if end > start && batchBytes+size > maxBatchBytes {
				break
			}
			batchBytes += size
			end++
		}
		buffers, err := s.downloadMediaBatch(ctx, plan.urls[start:end], plan.sizes[start:end])
		if err != nil {
			_ = os.Remove(temporary)
			return nil, err
		}
		for _, data := range buffers {
			available, err := freeBytes(s.cache.dir)
			if err != nil {
				_ = os.Remove(temporary)
				return nil, err
			}
			if available < s.cache.minFreeBytes || int64(len(data)) > available-s.cache.minFreeBytes {
				_ = os.Remove(temporary)
				return nil, fmt.Errorf("%w: free=%d minimum=%d", errCacheLimit, available, s.cache.minFreeBytes)
			}
			if err := writeAll(output, data); err != nil {
				_ = os.Remove(temporary)
				return nil, err
			}
			written += int64(len(data))
		}
		start = end
	}
	if written != plan.total {
		_ = os.Remove(temporary)
		return nil, fmt.Errorf("HLS size mismatch: wrote=%d expected=%d", written, plan.total)
	}
	if err := output.Sync(); err != nil {
		_ = os.Remove(temporary)
		return nil, err
	}
	if err := output.Close(); err != nil {
		_ = os.Remove(temporary)
		return nil, err
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)
		return nil, err
	}
	log.Printf("cached HLS track %s (%s)", id, destination)
	file, ok, err := s.openCachedMedia(destination)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("cached HLS file disappeared")
	}
	return file, nil
}

func (s *source) openCachedMedia(path string) (*os.File, bool, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, false, fmt.Errorf("cached media is not a regular file")
	}
	if info.Size() <= 0 || info.Size() > s.cache.maxBytes {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, false, nil
	}
	return file, true, nil
}

func serveCachedMedia(w http.ResponseWriter, r *http.Request, file *os.File) {
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		http.Error(w, "cached media unavailable", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func (s *source) streamLock(id string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock := s.streamLocks[id]
	if lock == nil {
		lock = &sync.Mutex{}
		s.streamLocks[id] = lock
	}
	return lock
}

func (s *source) fetch(ctx context.Context, target string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	response, err := s.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream status %d", response.StatusCode)
	}
	const maxPlaylistBytes = 4 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maxPlaylistBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPlaylistBytes {
		return nil, fmt.Errorf("playlist exceeds %d bytes", maxPlaylistBytes)
	}
	return data, nil
}

func (s *source) copyURLLimited(ctx context.Context, output io.Writer, target string, limit int64) (int64, error) {
	if limit <= 0 {
		return 0, errCacheLimit
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, err
	}
	response, err := s.httpClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("segment %s: status %d", target, response.StatusCode)
	}
	if response.ContentLength == 0 {
		return 0, fmt.Errorf("segment %s is empty", target)
	}
	if response.ContentLength > 0 {
		if err := s.cache.ensure(response.ContentLength); err != nil {
			return 0, err
		}
	}
	remainingBudget, err := s.cache.remainingBudget()
	if err != nil {
		return 0, err
	}
	if remainingBudget < limit {
		limit = remainingBudget
	}

	buffer := make([]byte, 64<<10)
	var written int64
	for {
		readSize := len(buffer)
		if remaining := limit - written; remaining < int64(readSize) {
			readSize = int(remaining) + 1
		}
		n, readErr := response.Body.Read(buffer[:readSize])
		if n > 0 {
			next := written + int64(n)
			if next > limit {
				return next, fmt.Errorf("%w: media exceeds remaining capacity", errCacheLimit)
			}
			available, err := freeBytes(s.cache.dir)
			if err != nil {
				return next, err
			}
			if available < s.cache.minFreeBytes || int64(n) > available-s.cache.minFreeBytes {
				return next, fmt.Errorf("%w: free=%d minimum=%d", errCacheLimit, available, s.cache.minFreeBytes)
			}
			if err := writeAll(output, buffer[:n]); err != nil {
				return next, err
			}
			written = next
		}
		if errors.Is(readErr, io.EOF) {
			if written == 0 {
				return 0, fmt.Errorf("segment %s is empty", target)
			}
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}

func (s *source) downloadMediaBatch(ctx context.Context, urls []string, sizes []int64) ([][]byte, error) {
	buffers := make([][]byte, len(urls))
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wait sync.WaitGroup
	var firstErr error
	var errOnce sync.Once
	for index := range urls {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			data, err := s.downloadMediaBytes(workerCtx, urls[index], sizes[index])
			if err != nil {
				errOnce.Do(func() {
					firstErr = err
					cancel()
				})
				return
			}
			buffers[index] = data
		}()
	}
	wait.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return buffers, nil
}

func (s *source) downloadMediaBytes(ctx context.Context, target string, expected int64) ([]byte, error) {
	if expected <= 0 || expected > 32<<20 {
		return nil, fmt.Errorf("invalid media segment size %d", expected)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	response, err := s.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("segment %s: status %d", target, response.StatusCode)
	}
	if response.ContentLength > 0 && response.ContentLength != expected {
		return nil, fmt.Errorf("segment %s: length changed from %d to %d", target, expected, response.ContentLength)
	}
	if encoding := response.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return nil, fmt.Errorf("segment %s: unexpected encoding %q", target, encoding)
	}

	data := make([]byte, int(expected))
	if _, err := io.ReadFull(response.Body, data); err != nil {
		return nil, fmt.Errorf("segment %s: %w", target, err)
	}
	var extra [1]byte
	for {
		n, err := response.Body.Read(extra[:])
		if n != 0 {
			return nil, fmt.Errorf("segment %s: response exceeds content length", target)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return data, nil
}

func writeAll(output io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := output.Write(data)
		data = data[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

type hlsPlaylist struct {
	base           *url.URL
	initialization string
	segments       []string
}

func newHLSPlaylist(raw string) *hlsPlaylist {
	base, _ := url.Parse(raw)
	return &hlsPlaylist{base: base}
}

func (p *hlsPlaylist) parse(data []byte) error {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") || strings.HasPrefix(line, "#EXT-X-MEDIA:") {
			return fmt.Errorf("master HLS playlist is unsupported")
		}
		if strings.HasPrefix(line, "#EXT-X-BYTERANGE:") {
			return fmt.Errorf("byte-range HLS playlist is unsupported")
		}
		if strings.HasPrefix(line, "#EXT-X-KEY:") &&
			!strings.Contains(strings.ToUpper(line), "METHOD=NONE") {
			return fmt.Errorf("encrypted HLS playlist")
		}
		if strings.HasPrefix(line, "#EXT-X-MAP:") {
			if attribute(line, "BYTERANGE") != "" {
				return fmt.Errorf("byte-range HLS initialization is unsupported")
			}
			if value := attribute(line, "URI"); value != "" {
				p.initialization = p.resolve(value)
			}
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p.segments = append(p.segments, p.resolve(line))
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(p.segments) == 0 {
		return fmt.Errorf("HLS playlist has no segments")
	}
	return nil
}

func (p *hlsPlaylist) resolve(reference string) string {
	parsed, err := url.Parse(reference)
	if err != nil {
		return reference
	}
	return p.base.ResolveReference(parsed).String()
}

func attribute(line, key string) string {
	prefix := key + "=\""
	index := strings.Index(line, prefix)
	if index < 0 {
		return ""
	}
	value := line[index+len(prefix):]
	if end := strings.IndexByte(value, '"'); end >= 0 {
		return value[:end]
	}
	return ""
}

func (s *source) artworkFor(track sc.Track) []byte {
	artworkURL := track.Artwork
	if artworkURL == "" {
		artworkURL = track.Author.Avatar
	}
	if artworkURL == "" {
		return nil
	}
	artworkURL = strings.Replace(artworkURL, "-large.", "-t200x200.", 1)
	id := string(track.ID)
	s.mu.RLock()
	cached := s.artwork[id]
	s.mu.RUnlock()
	if cached != nil {
		return cached
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artworkURL, nil)
	if err != nil {
		return nil
	}
	response, err := s.httpClient.Do(request)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil || len(data) == 0 {
		return nil
	}
	s.mu.Lock()
	s.artwork[id] = data
	s.mu.Unlock()
	return data
}
