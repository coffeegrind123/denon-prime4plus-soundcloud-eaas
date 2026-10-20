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

func TestTrackBlobPathIncludesExtension(t *testing.T) {
	if got := trackBlobPath("123", "m4a"); got != `<C:\SoundCloud\123.m4a>` {
		t.Fatalf("blob path = %q", got)
	}
}

func TestRootMetadataIncludesLikedPlaylists(t *testing.T) {
	source := &source{
		user:           sc.User{Liked: 10},
		userPlaylists:  map[string]sc.Playlist{},
		likedPlaylists: map[string]sc.Playlist{},
		likedOrder: []sc.Playlist{{
			Permalink:  "liked-list",
			Title:      "Liked List",
			TrackCount: 3,
		}},
	}
	root := source.rootMetadata()
	if len(root.Playlists) != 4 {
		t.Fatalf("root playlists = %d", len(root.Playlists))
	}
	likes := root.Playlists[1]
	if len(likes.Playlists) != 2 || likes.Playlists[1].GetId() != likedPlaylistsID {
		t.Fatalf("liked groups = %+v", likes.Playlists)
	}
	liked := likes.Playlists[1]
	if len(liked.Playlists) != 1 || liked.Playlists[0].GetTitle() != "Liked List" {
		t.Fatalf("liked playlists = %+v", liked.Playlists)
	}
}

func TestPlaylistKeyIncludesAuthor(t *testing.T) {
	first := sc.Playlist{Permalink: "same-name"}
	first.Author.Permalink = "first-author"
	second := sc.Playlist{Permalink: "same-name"}
	second.Author.Permalink = "second-author"

	if playlistKey(first) == playlistKey(second) {
		t.Fatal("playlists from different authors collided")
	}
}
