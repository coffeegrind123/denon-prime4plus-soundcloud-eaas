package main

import (
	"testing"

	"git.maid.zone/stuff/soundcloak/lib/sc"
)

func TestMetadataForTrack(t *testing.T) {
	track := sc.Track{
		Title: "Example",
		Genre: "House",
	}
	track.ID = "123"
	track.Duration = 245000
	track.Author.Username = "Artist"

	metadata := metadataForTrack(track)
	if metadata.GetId() != "123" || metadata.GetTitle() != "Example" {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
	if metadata.GetArtist() != "Artist" || metadata.GetLengthSeconds() != 245 {
		t.Fatalf("unexpected artist/length: %+v", metadata)
	}
}

func TestPlaylistMetadata(t *testing.T) {
	metadata := playlistMetadata("likes", "Likes", 42)
	if metadata.GetId() != "likes" || metadata.GetTitle() != "Likes" {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
	if metadata.GetTrackCount() != 42 {
		t.Fatalf("track count = %d", metadata.GetTrackCount())
	}
}
