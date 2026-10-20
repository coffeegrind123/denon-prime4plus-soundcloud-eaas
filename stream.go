package main

import (
	"bufio"
	"bytes"
	"context"
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
	if id == "" || id == "." || id == "/" {
		http.Error(w, "missing track id", http.StatusBadRequest)
		return
	}

	stream, err := s.resolveStream(id)
	if err != nil {
		log.Printf("resolve stream %s: %v", id, err)
		http.Error(w, "stream unavailable", http.StatusBadGateway)
		return
	}

	if stream.protocol == sc.ProtocolProgressive {
		s.proxyStream(w, r, stream.url)
		return
	}

	file, err := s.materializeHLS(id, stream)
	if err != nil {
		log.Printf("materialize HLS %s: %v", id, err)
		http.Error(w, "stream unavailable", http.StatusBadGateway)
		return
	}
	http.ServeFile(w, r, file)
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

func (s *source) proxyStream(w http.ResponseWriter, r *http.Request, target string) {
	request, err := http.NewRequestWithContext(r.Context(), r.Method, target, nil)
	if err != nil {
		http.Error(w, "bad upstream request", http.StatusBadGateway)
		return
	}
	for _, header := range []string{"Range", "If-Range"} {
		if value := r.Header.Get(header); value != "" {
			request.Header.Set(header, value)
		}
	}
	response, err := s.httpClient.Do(request)
	if err != nil {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()

	for _, header := range []string{
		"Accept-Ranges", "Content-Length", "Content-Range", "Content-Type", "ETag", "Last-Modified",
	} {
		if value := response.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, response.Body)
	}
}

func (s *source) materializeHLS(id string, stream resolvedStream) (string, error) {
	lock := s.streamLock(id)
	lock.Lock()
	defer lock.Unlock()

	extension := stream.ext
	if extension == "" {
		extension = "m4a"
	}
	destination := filepath.Join(s.cacheDir, id+"."+extension)
	if info, err := os.Stat(destination); err == nil && info.Size() > 0 {
		return destination, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	playlist, err := s.fetch(ctx, stream.url)
	if err != nil {
		return "", err
	}
	parser := newHLSPlaylist(stream.url)
	if err := parser.parse(playlist); err != nil {
		_ = os.Remove(destination + ".part")
		return "", err
	}

	temporary := destination + ".part"
	output, err := os.Create(temporary)
	if err != nil {
		return "", err
	}
	defer output.Close()

	if parser.initialization != "" {
		if err := s.copyURL(ctx, output, parser.initialization); err != nil {
			_ = os.Remove(temporary)
			return "", err
		}
	}
	for _, segment := range parser.segments {
		if err := s.copyURL(ctx, output, segment); err != nil {
			_ = os.Remove(temporary)
			return "", err
		}
	}
	if err := output.Sync(); err != nil {
		_ = os.Remove(temporary)
		return "", err
	}
	if err := output.Close(); err != nil {
		_ = os.Remove(temporary)
		return "", err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return "", err
	}
	log.Printf("cached HLS track %s (%s)", id, destination)
	return destination, nil
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
	return io.ReadAll(io.LimitReader(response.Body, 4<<20))
}

func (s *source) copyURL(ctx context.Context, output io.Writer, target string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	response, err := s.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("segment %s: status %d", target, response.StatusCode)
	}
	_, err = io.Copy(output, response.Body)
	return err
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
		if strings.HasPrefix(line, "#EXT-X-KEY:") &&
			!strings.Contains(strings.ToUpper(line), "METHOD=NONE") {
			return fmt.Errorf("encrypted HLS playlist")
		}
		if strings.HasPrefix(line, "#EXT-X-MAP:") {
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
