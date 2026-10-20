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

	file, err := s.materializeHLS(r.Context(), id, stream)
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
	var file *os.File
	if stream.protocol == sc.ProtocolProgressive {
		file, err = s.materializeProgressive(ctx, id, stream)
	} else {
		file, err = s.materializeHLS(ctx, id, stream)
	}
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return 0, "", err
	}
	if stat.Size() <= 0 || stat.Size() > int64(^uint32(0)) {
		return 0, "", fmt.Errorf("invalid cached media size %d", stat.Size())
	}
	return uint32(stat.Size()), extension, nil
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

	if err := s.cache.beginDownload(ctx); err != nil {
		return nil, err
	}
	defer s.cache.endDownload()

	if file, ok, err := s.openCachedMedia(destination); ok || err != nil {
		return file, err
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
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

func (s *source) materializeHLS(ctx context.Context, id string, stream resolvedStream) (*os.File, error) {
	lock := s.streamLock(id)
	lock.Lock()
	defer lock.Unlock()

	extension := stream.ext
	if extension == "" {
		extension = "m4a"
	}
	destination := filepath.Join(s.cache.dir, id+"."+extension)

	if err := s.cache.beginDownload(ctx); err != nil {
		return nil, err
	}
	defer s.cache.endDownload()

	if file, ok, err := s.openCachedMedia(destination); ok || err != nil {
		return file, err
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	playlist, err := s.fetch(ctx, stream.url)
	if err != nil {
		return nil, err
	}
	parser := newHLSPlaylist(stream.url)
	if err := parser.parse(playlist); err != nil {
		return nil, err
	}

	temporary := destination + ".part"
	output, err := os.Create(temporary)
	if err != nil {
		return nil, err
	}
	defer output.Close()
	var written int64

	if parser.initialization != "" {
		n, err := s.copyURLLimited(ctx, output, parser.initialization, s.cache.maxBytes)
		written += n
		if err != nil {
			_ = os.Remove(temporary)
			return nil, err
		}
	}
	for _, segment := range parser.segments {
		n, err := s.copyURLLimited(ctx, output, segment, s.cache.maxBytes)
		written += n
		if err != nil {
			_ = os.Remove(temporary)
			return nil, err
		}
	}
	if written == 0 {
		_ = os.Remove(temporary)
		return nil, fmt.Errorf("HLS media is empty")
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
