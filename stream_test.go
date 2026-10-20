package main

import "testing"

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

func TestAttribute(t *testing.T) {
	line := `#EXT-X-MAP:URI="init.mp4",BYTERANGE="720@0"`
	if got := attribute(line, "URI"); got != "init.mp4" {
		t.Fatalf("attribute = %q", got)
	}
}
