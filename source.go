package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"git.maid.zone/stuff/soundcloak/lib/cfg"
	"git.maid.zone/stuff/soundcloak/lib/sc"
	"github.com/icedream/go-stagelinq/eaas/proto/enginelibrary"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	libraryID        = "soundcloud-source"
	rootID           = "profile"
	tracksID         = "tracks"
	likesID          = "likes"
	likedTracksID    = "likes-tracks"
	likedPlaylistsID = "likes-playlists"
	repostsID        = "reposts"
	playlistsID      = "playlists"
)

type source struct {
	enginelibrary.UnimplementedEngineLibraryServiceServer

	profile    string
	display    string
	hostIP     string
	cache      *cacheManager
	feedMax    int
	httpClient *http.Client
	user       sc.User

	mu             sync.RWMutex
	tracks         map[string]sc.Track
	userPlaylists  map[string]sc.Playlist
	likedPlaylists map[string]sc.Playlist
	recentLiked    []sc.Playlist
	playlistOrder  []string
	artwork        map[string][]byte
	streamLocks    map[string]*sync.Mutex
}

func newSource(profile, display, hostIP, cacheDir string, feedMax int, cacheMax, cacheMin int64) (*source, error) {
	user, err := sc.GetUser(profile)
	if err != nil {
		return nil, fmt.Errorf("resolve user: %w", err)
	}
	cache, err := newCacheManager(cacheDir, cacheMax, cacheMin)
	if err != nil {
		return nil, fmt.Errorf("cache setup: %w", err)
	}

	s := &source{
		profile:        profile,
		display:        display,
		hostIP:         hostIP,
		cache:          cache,
		feedMax:        feedMax,
		httpClient:     &http.Client{},
		user:           user,
		tracks:         map[string]sc.Track{},
		userPlaylists:  map[string]sc.Playlist{},
		likedPlaylists: map[string]sc.Playlist{},
		artwork:        map[string][]byte{},
		streamLocks:    map[string]*sync.Mutex{},
	}

	playlists, err := user.GetPlaylists(cfg.DefaultPreferences, "limit=50&linked_partitioning=1")
	if err != nil {
		log.Printf("load user playlists: %v", err)
	} else {
		for {
			for _, playlist := range playlists.Collection {
				s.cachePlaylist(*playlist)
				s.playlistOrder = append(s.playlistOrder, playlist.Permalink)
			}
			if playlists.NextHref == "" {
				break
			}
			if err := playlists.Proceed(true); err != nil {
				log.Printf("load user playlists page: %v", err)
				break
			}
		}
	}

	likes, err := user.GetLikes(cfg.DefaultPreferences, "limit=50&linked_partitioning=1")
	if err != nil {
		log.Printf("load liked playlists: %v", err)
	} else {
		for _, like := range likes.Collection {
			if like.Playlist == nil {
				continue
			}
			s.cachePlaylist(*like.Playlist)
			s.likedPlaylists[like.Playlist.Permalink] = *like.Playlist
			if len(s.recentLiked) < 20 {
				s.recentLiked = append(s.recentLiked, *like.Playlist)
			}
		}
	}

	return s, nil
}

func (s *source) cachePlaylist(playlist sc.Playlist) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.userPlaylists[playlist.Permalink] = playlist
}

func (s *source) cacheTrack(track sc.Track) {
	id := string(track.ID)
	if id == "" || id == "0" {
		return
	}
	s.mu.Lock()
	s.tracks[id] = track
	s.mu.Unlock()
}

func (s *source) GetLibraries(context.Context, *enginelibrary.GetLibrariesRequest) (*enginelibrary.GetLibrariesResponse, error) {
	log.Printf("GetLibraries")
	id := libraryID
	title := s.display
	logo := enginelibrary.LibraryLogo_LIBRARY_LOGO_UNSPECIFIED
	return &enginelibrary.GetLibrariesResponse{
		Libraries: []*enginelibrary.Library{{
			Id:    &id,
			Title: &title,
			Logo:  &logo,
		}},
	}, nil
}

func (s *source) GetLibrary(_ context.Context, req *enginelibrary.GetLibraryRequest) (*enginelibrary.GetLibraryResponse, error) {
	log.Printf("GetLibrary library=%q", req.GetLibraryId())
	if req.GetLibraryId() != "" && req.GetLibraryId() != libraryID {
		return nil, status.Error(codes.NotFound, "unknown library")
	}

	root := s.rootMetadata()
	return &enginelibrary.GetLibraryResponse{Playlists: root.Playlists}, nil
}

func (s *source) rootMetadata() *enginelibrary.PlaylistMetadata {
	profileID := rootID
	profileTitle := s.user.Username
	profileCount := uint32(s.user.Tracks + s.user.Liked)

	s.mu.RLock()
	defer s.mu.RUnlock()

	likedChildren := make([]*enginelibrary.PlaylistMetadata, 0, 2+len(s.recentLiked))
	likedChildren = append(likedChildren,
		playlistMetadata(likedTracksID, "Liked Tracks", uint32(s.user.Liked)),
	)
	likedPlaylistChildren := make([]*enginelibrary.PlaylistMetadata, 0, len(s.recentLiked))
	for _, playlist := range s.recentLiked {
		likedPlaylistChildren = append(likedPlaylistChildren,
			s.playlistMetadataLocked(playlist),
		)
	}
	likedPlaylistParent := playlistMetadata(likedPlaylistsID, "Liked Playlists", uint32(len(likedPlaylistChildren)))
	likedPlaylistParent.Playlists = likedPlaylistChildren
	likedChildren = append(likedChildren, likedPlaylistParent)

	userPlaylistChildren := make([]*enginelibrary.PlaylistMetadata, 0, len(s.playlistOrder))
	for _, id := range s.playlistOrder {
		if playlist, ok := s.userPlaylists[id]; ok {
			userPlaylistChildren = append(userPlaylistChildren,
				s.playlistMetadataLocked(playlist),
			)
		}
	}

	playlistsParent := playlistMetadata(playlistsID, "Playlists", uint32(len(userPlaylistChildren)))
	playlistsParent.Playlists = userPlaylistChildren

	likesParent := playlistMetadata(likesID, "Likes", uint32(s.user.Liked))
	likesParent.Playlists = likedChildren

	root := &enginelibrary.PlaylistMetadata{
		Id:         &profileID,
		Title:      &profileTitle,
		TrackCount: &profileCount,
		Playlists: []*enginelibrary.PlaylistMetadata{
			playlistMetadata(tracksID, "My Tracks", uint32(s.user.Tracks)),
			likesParent,
			playlistMetadata(repostsID, "Reposts", 0),
			playlistsParent,
		},
	}
	return root
}

func playlistMetadata(id, title string, count uint32) *enginelibrary.PlaylistMetadata {
	title = strings.ToValidUTF8(title, "\uFFFD")
	listType := enginelibrary.ListType_LIST_TYPE_PLAY
	return &enginelibrary.PlaylistMetadata{
		Id:         &id,
		Title:      &title,
		TrackCount: &count,
		ListType:   &listType,
	}
}

func (s *source) playlistMetadataLocked(playlist sc.Playlist) *enginelibrary.PlaylistMetadata {
	id := "sc-playlist:" + playlist.Permalink
	return playlistMetadata(id, playlist.Title, uint32(playlist.TrackCount))
}

func (s *source) GetTracks(req *enginelibrary.GetTracksRequest, stream grpc.ServerStreamingServer[enginelibrary.GetTracksResponse]) error {
	limit := s.feedMax
	if limit <= 0 || limit > 200 {
		limit = 200
	}

	tracks, err := s.fetchPlaylistTracks(req.GetPlaylistId(), limit)
	if err != nil {
		log.Printf("GetTracks(%q): %v", req.GetPlaylistId(), err)
		return status.Error(codes.Unavailable, err.Error())
	}
	log.Printf("GetTracks playlist=%q limit=%d returned=%d", req.GetPlaylistId(), limit, len(tracks))
	return s.streamTracks(stream, tracks)
}

func (s *source) fetchPlaylistTracks(playlistID string, limit int) ([]sc.Track, error) {
	switch {
	case playlistID == "" || playlistID == rootID || playlistID == playlistsID || playlistID == likedPlaylistsID:
		return nil, nil
	case playlistID == tracksID:
		return collectUserTracks(s.user, limit)
	case playlistID == likesID || playlistID == likedTracksID:
		return collectLikes(s.user, limit)
	case playlistID == repostsID:
		return collectReposts(s.user, limit)
	case strings.HasPrefix(playlistID, "sc-playlist:"):
		id := strings.TrimPrefix(playlistID, "sc-playlist:")
		return s.fetchPlaylist(id, limit)
	default:
		return nil, nil
	}
}

func collectUserTracks(user sc.User, limit int) ([]sc.Track, error) {
	page, err := user.GetTracks(cfg.DefaultPreferences, fmt.Sprintf("limit=%d&linked_partitioning=1", min(limit, 50)))
	if err != nil {
		return nil, err
	}
	out := make([]sc.Track, 0, limit)
	for {
		for _, track := range page.Collection {
			out = append(out, *track)
			if len(out) >= limit {
				return out, nil
			}
		}
		if page.NextHref == "" {
			return out, nil
		}
		if err := page.Proceed(true); err != nil {
			return out, err
		}
	}
}

func collectLikes(user sc.User, limit int) ([]sc.Track, error) {
	page, err := user.GetLikes(cfg.DefaultPreferences, "limit=50&linked_partitioning=1")
	if err != nil {
		return nil, err
	}
	out := make([]sc.Track, 0, limit)
	for {
		for _, like := range page.Collection {
			if like.Track == nil {
				continue
			}
			out = append(out, *like.Track)
			if len(out) >= limit {
				return out, nil
			}
		}
		if page.NextHref == "" || len(out) >= limit {
			return out, nil
		}
		if err := page.Proceed(true); err != nil {
			return out, err
		}
	}
}

func collectReposts(user sc.User, limit int) ([]sc.Track, error) {
	page, err := user.GetReposts(cfg.DefaultPreferences, "limit=50&linked_partitioning=1")
	if err != nil {
		return nil, err
	}
	out := make([]sc.Track, 0, limit)
	for {
		for _, repost := range page.Collection {
			if repost.Track == nil {
				continue
			}
			out = append(out, *repost.Track)
			if len(out) >= limit {
				return out, nil
			}
		}
		if page.NextHref == "" || len(out) >= limit {
			return out, nil
		}
		if err := page.Proceed(true); err != nil {
			return out, err
		}
	}
}

func (s *source) fetchPlaylist(id string, limit int) ([]sc.Track, error) {
	s.mu.RLock()
	playlist, ok := s.userPlaylists[id]
	s.mu.RUnlock()
	if !ok {
		s.mu.RLock()
		playlist, ok = s.likedPlaylists[id]
		s.mu.RUnlock()
	}

	if !ok {
		return nil, fmt.Errorf("unknown SoundCloud playlist %s", id)
	}
	path := s.profile + "/sets/" + playlist.Permalink
	if playlist.Permalink == "" {
		return nil, fmt.Errorf("playlist %s has no permalink", id)
	}
	resolved, err := sc.GetPlaylist(path)
	if err != nil {
		return nil, err
	}
	if err := resolved.Fix(true, true); err != nil {
		return nil, err
	}

	out := make([]sc.Track, 0, min(limit, len(resolved.Tracks)))
	for i := range resolved.Tracks {
		out = append(out, resolved.Tracks[i])
		if len(out) >= limit {
			break
		}
	}
	s.mu.Lock()
	s.userPlaylists[id] = resolved
	s.mu.Unlock()
	return out, nil
}

func (s *source) toListTracks(tracks []sc.Track, artworkLimit int) []*enginelibrary.ListTrack {
	for i := range tracks {
		s.cacheTrack(tracks[i])
	}

	out := make([]*enginelibrary.ListTrack, len(tracks))
	var wg sync.WaitGroup
	slots := make(chan struct{}, 8)
	for i := range tracks {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			out[i] = &enginelibrary.ListTrack{Metadata: metadataForTrack(tracks[i])}
			if i < artworkLimit {
				out[i].PreviewArtwork = s.artworkFor(tracks[i])
			}
		}()
	}
	wg.Wait()
	return out
}

func metadataForTrack(track sc.Track) *enginelibrary.TrackMetadata {
	id := strings.ToValidUTF8(string(track.ID), "")
	title := strings.ToValidUTF8(track.Title, "\uFFFD")
	artist := strings.ToValidUTF8(track.Author.Username, "\uFFFD")
	genre := strings.ToValidUTF8(track.Genre, "\uFFFD")
	label := strings.ToValidUTF8(track.PublisherMetadata.ISRC, "\uFFFD")
	comment := strings.ToValidUTF8(truncate(track.Description, 2048), "\uFFFD")
	length := track.Duration / 1000
	metadata := &enginelibrary.TrackMetadata{
		Id:            &id,
		Title:         &title,
		Artist:        &artist,
		LengthSeconds: &length,
	}
	if genre != "" {
		metadata.Genre = &genre
	}
	if label != "" {
		metadata.Label = &label
	}
	if comment != "" {
		metadata.Comment = &comment
	}
	if timestamp, err := time.Parse(time.RFC3339, track.CreatedAt); err == nil {
		metadata.DateAdded = timestamppb.New(timestamp)
	}
	return metadata
}

func (s *source) GetSearchFilters(_ context.Context, req *enginelibrary.GetSearchFiltersRequest) (*enginelibrary.GetSearchFiltersResponse, error) {
	query := strings.ToLower(strings.TrimSpace(req.GetQuery()))
	candidates := []sc.Track{}
	if len(query) >= 2 {
		values := url.Values{}
		values.Set("q", query)
		values.Set("limit", "50")
		values.Set("linked_partitioning", "1")
		result, err := sc.SearchTracks(cfg.DefaultPreferences, []byte(values.Encode()))
		if err == nil {
			for _, track := range result.Collection {
				candidates = append(candidates, *track)
				s.cacheTrack(*track)
			}
		}
	} else {
		s.mu.RLock()
		for _, track := range s.tracks {
			candidates = append(candidates, track)
		}
		s.mu.RUnlock()
	}

	options := &enginelibrary.SearchFilterOptions{
		Genres:  distinctValues(candidates, func(track sc.Track) string { return track.Genre }),
		Artists: distinctValues(candidates, func(track sc.Track) string { return track.Author.Username }),
	}
	return &enginelibrary.GetSearchFiltersResponse{SearchFilters: options}, nil
}

func distinctValues(tracks []sc.Track, value func(sc.Track) string) []*enginelibrary.SearchFilterValue {
	seen := map[string]struct{}{}
	for _, track := range tracks {
		v := strings.ToValidUTF8(value(track), "\uFFFD")
		if v = strings.TrimSpace(v); v != "" {
			seen[v] = struct{}{}
		}
	}
	values := make([]string, 0, len(seen))
	for value := range seen {
		values = append(values, value)
	}
	sort.Strings(values)
	out := make([]*enginelibrary.SearchFilterValue, len(values))
	for i, value := range values {
		value := value
		out[i] = &enginelibrary.SearchFilterValue{Value: &value}
	}
	return out
}

func (s *source) SearchTracks(req *enginelibrary.SearchTracksRequest, stream grpc.ServerStreamingServer[enginelibrary.SearchTracksResponse]) error {
	query := strings.TrimSpace(req.GetQuery())
	if len(query) < 2 {
		return stream.Send(&enginelibrary.SearchTracksResponse{})
	}
	limit := int(req.GetPageSize())
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	values := url.Values{}
	values.Set("q", query)
	values.Set("limit", fmt.Sprintf("%d", min(limit, 50)))
	values.Set("linked_partitioning", "1")
	result, err := sc.SearchTracks(cfg.DefaultPreferences, []byte(values.Encode()))
	if err != nil {
		return status.Error(codes.Unavailable, err.Error())
	}
	tracks := make([]sc.Track, 0, len(result.Collection))
	for _, track := range result.Collection {
		tracks = append(tracks, *track)
		if len(tracks) >= limit {
			break
		}
	}
	log.Printf("SearchTracks query=%q returned=%d", query, len(tracks))
	return s.streamSearchTracks(stream, tracks)
}

func (s *source) GetTrack(ctx context.Context, req *enginelibrary.GetTrackRequest) (*enginelibrary.GetTrackResponse, error) {
	log.Printf("GetTrack library=%q track=%q", req.GetLibraryId(), req.GetTrackId())
	track, err := s.track(req.GetTrackId())
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	size, extension, err := s.prepareTrack(ctx, string(track.ID))
	if err != nil {
		log.Printf("GetTrack prepare %s: %v", track.ID, err)
		return nil, status.Error(codes.Unavailable, "track stream unavailable")
	}
	url := trackBlobPath(string(track.ID), extension)
	metadata := metadataForTrack(track)
	unset := -1.0
	log.Printf("GetTrack prepared track=%s size=%d path=%s", track.ID, size, url)
	return &enginelibrary.GetTrackResponse{
		Blob: &enginelibrary.TrackBlob{
			Type: &enginelibrary.TrackBlob_Url{
				Url: &enginelibrary.TrackBlobUrl{Url: &url, FileSize: &size},
			},
		},
		PerformanceData: &enginelibrary.TrackPerformanceData{
			Bpm: metadata.Bpm,
			MainCue: &enginelibrary.MainCue{
				Position:        &unset,
				InitialPosition: &unset,
			},
		},
		Metadata: metadata,
	}, nil
}

func (s *source) track(id string) (sc.Track, error) {
	s.mu.RLock()
	track, ok := s.tracks[id]
	s.mu.RUnlock()
	if ok {
		return track, nil
	}
	track, err := sc.GetTrackByID(id)
	if err != nil {
		return sc.Track{}, err
	}
	s.cacheTrack(track)
	return track, nil
}

func (s *source) GetHistorySessions(context.Context, *enginelibrary.GetHistorySessionsRequest) (*enginelibrary.GetHistorySessionsResponse, error) {
	return &enginelibrary.GetHistorySessionsResponse{}, nil
}

func (s *source) GetHistoryPlayedTracks(context.Context, *enginelibrary.GetHistoryPlayedTracksRequest) (*enginelibrary.GetHistoryPlayedTracksResponse, error) {
	return &enginelibrary.GetHistoryPlayedTracksResponse{}, nil
}

func (s *source) PutEvents(context.Context, *enginelibrary.PutEventsRequest) (*enginelibrary.PutEventsResponse, error) {
	return &enginelibrary.PutEventsResponse{}, nil
}

func (s *source) EventStream(*enginelibrary.EventStreamRequest, grpc.ServerStreamingServer[enginelibrary.EventStreamResponse]) error {
	return nil
}

func (s *source) GetCredentials(context.Context, *enginelibrary.GetCredentialsRequest) (*enginelibrary.GetCredentialsResponse, error) {
	return &enginelibrary.GetCredentialsResponse{}, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}

func trackBlobPath(id, extension string) string {
	return fmt.Sprintf("<C:\\SoundCloud\\%s.%s>", id, extension)
}

func (s *source) streamTracks(stream grpc.ServerStreamingServer[enginelibrary.GetTracksResponse], tracks []sc.Track) error {
	err := stream.Send(&enginelibrary.GetTracksResponse{Tracks: s.toListTracks(tracks, 50)})
	if err != nil {
		log.Printf("GetTracks stream send: %v", err)
	}
	return err
}

func (s *source) streamSearchTracks(stream grpc.ServerStreamingServer[enginelibrary.SearchTracksResponse], tracks []sc.Track) error {
	err := stream.Send(&enginelibrary.SearchTracksResponse{Tracks: s.toListTracks(tracks, 50)})
	if err != nil {
		log.Printf("SearchTracks stream send: %v", err)
	}
	return err
}
