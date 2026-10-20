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
	log.Printf("download track=%s range=%q", id, r.Header.Get("Range"))

	stream, err := s.resolveStream(id)
	if err != nil {
		log.Printf("resolve stream %s: %v", id, err)
		http.Error(w, "stream unavailable", http.StatusBadGateway)
		return
	}

	if stream.protocol == sc.ProtocolProgressive {
		file, err := s.materializeProgressive(id, stream)
		if err != nil {
			log.Printf("materialize progressive %s: %v", id, err)
			http.Error(w, "stream unavailable", http.StatusBadGateway)
			return
		}
		http.ServeFile(w, r, file)
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

func (s *source) prepareTrack(id string) (uint32, string, error) {
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
	if stream.protocol == sc.ProtocolProgressive {
		info, err := s.progressiveSize(stream.url)
		if err == nil && info > 0 {
			return info, extension, nil
		}
		file, err := s.materializeProgressive(id, stream)
		if err != nil {
			return 0, "", err
		}
		stat, err := os.Stat(file)
		if err != nil {
			return 0, "", err
		}
		return uint32(stat.Size()), extension, nil
	}

	file, err := s.materializeHLS(id, stream)
	if err != nil {
		return 0, "", err
	}
	stat, err := os.Stat(file)
	if err != nil {
		return 0, "", err
	}
	return uint32(stat.Size()), extension, nil
}

func (s *source) progressiveSize(target string) (uint32, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
	if err != nil {
		return 0, err
	}
	response, err := s.httpClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return 0, fmt.Errorf("HEAD status %d", response.StatusCode)
	}
	if response.ContentLength <= 0 || response.ContentLength > int64(^uint32(0)) {
		return 0, fmt.Errorf("invalid content length")
	}
	return uint32(response.ContentLength), nil
}

func (s *source) materializeProgressive(id string, stream resolvedStream) (string, error) {
	lock := s.streamLock(id)
	lock.Lock()
	defer lock.Unlock()

	extension := stream.ext
	if extension == "" {
		extension = "mp3"
	}
	destination := filepath.Join(s.cacheDir, id+"."+extension)
	if info, err := os.Stat(destination); err == nil && info.Size() > 0 {
		return destination, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	temporary := destination + ".part"
	output, err := os.Create(temporary)
	if err != nil {
		return "", err
	}
	if err := s.copyURL(ctx, output, stream.url); err != nil {
		_ = output.Close()
		_ = os.Remove(temporary)
		return "", err
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
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
	log.Printf("cached progressive track %s (%s)", id, destination)
	return destination, nil
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
