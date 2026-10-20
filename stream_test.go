package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMediaMaterializeContextSurvivesClientCancellation(t *testing.T) {
	client, cancelClient := context.WithCancel(context.Background())
	materialize, cancelMaterialize := mediaMaterializeContext(client)
	defer cancelMaterialize()

	cancelClient()
	select {
	case <-materialize.Done():
		t.Fatal("materialization inherited the short client deadline")
	default:
	}

	deadline, ok := materialize.Deadline()
	if !ok || time.Until(deadline) < time.Minute {
		t.Fatal("materialization deadline is too short")
	}
}

func TestHLSPlaylistParse(t *testing.T) {
	playlist := newHLSPlaylist("https://cdn.example.test/path/master.m3u8")
	err := playlist.parse([]byte(
		"#EXTM3U\n" +
			"#EXT-X-MAP:URI=\"init.mp4\"\n" +
			"#EXTINF:2,\n" +
			"segment-1.m4s\n" +
			"#EXTINF:2,\n" +
			"../segment-2.m4s\n",
	))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if playlist.initialization != "https://cdn.example.test/path/init.mp4" {
		t.Fatalf("initialization = %q", playlist.initialization)
	}
	if len(playlist.segments) != 2 {
		t.Fatalf("segments = %d", len(playlist.segments))
	}
	if playlist.segments[1] != "https://cdn.example.test/segment-2.m4s" {
		t.Fatalf("segment = %q", playlist.segments[1])
	}
}

func TestHLSPlaylistRejectsEncryption(t *testing.T) {
	playlist := newHLSPlaylist("https://cdn.example.test/master.m3u8")
	err := playlist.parse([]byte(
		"#EXTM3U\n" +
			"#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\"\n" +
			"#EXTINF:2,\nsegment.m4s\n",
	))
	if err == nil {
		t.Fatal("expected encrypted playlist to be rejected")
	}
}

func TestHLSPlaylistRejectsMasterAndByteRanges(t *testing.T) {
	tests := []string{
		"#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=128000\nstream.m3u8\n",
		"#EXTM3U\n#EXT-X-BYTERANGE:100@0\nsegment.m4s\n",
		"#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\",BYTERANGE=\"100@0\"\n#EXTINF:2,\nsegment.m4s\n",
	}
	for _, playlist := range tests {
		parser := newHLSPlaylist("https://cdn.example.test/master.m3u8")
		if err := parser.parse([]byte(playlist)); err == nil {
			t.Fatalf("expected playlist rejection: %q", playlist)
		}
	}
}

func TestAttribute(t *testing.T) {
	line := `#EXT-X-MAP:URI="init.mp4",BYTERANGE="720@0"`
	if got := attribute(line, "URI"); got != "init.mp4" {
		t.Fatalf("attribute = %q", got)
	}
}

func TestHLSSizeUsesSegmentContentLengths(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/master.m3u8":
			_, _ = fmt.Fprint(w,
				"#EXTM3U\n"+
					"#EXT-X-MAP:URI=\"init.mp4\"\n"+
					"#EXTINF:2,\nsegment-1.m4s\n"+
					"#EXTINF:2,\nsegment-2.m4s\n")
		case "/init.mp4":
			w.Header().Set("Content-Length", "100")
		case "/segment-1.m4s":
			w.Header().Set("Content-Length", "200")
		case "/segment-2.m4s":
			w.Header().Set("Content-Length", "300")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	source := &source{httpClient: server.Client()}
	size, err := source.hlsSize(context.Background(), server.URL+"/master.m3u8")
	if err != nil {
		t.Fatalf("hls size: %v", err)
	}
	if size != 600 {
		t.Fatalf("size = %d", size)
	}
}
