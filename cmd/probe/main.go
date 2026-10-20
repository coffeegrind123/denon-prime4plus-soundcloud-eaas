package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/icedream/go-stagelinq/eaas/proto/enginelibrary"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	address := flag.String("address", "192.168.4.39:50010", "EaaS gRPC address")
	trackID := flag.String("track-id", "", "Prepare one track and exit")
	playlistID := flag.String("playlist-id", "", "List one playlist and exit")
	timeout := flag.Duration("timeout", 15*time.Second, "RPC timeout")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	connection, err := grpc.NewClient(*address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal(err)
	}
	defer connection.Close()
	client := enginelibrary.NewEngineLibraryServiceClient(connection)

	if *trackID != "" {
		result, err := client.GetTrack(ctx, &enginelibrary.GetTrackRequest{
			LibraryId: stringPtr("soundcloud-source"),
			TrackId:   trackID,
		})
		if err != nil {
			log.Fatal("GetTrack: ", err)
		}
		fmt.Printf("stream=%q size=%d\n",
			result.GetBlob().GetUrl().GetUrl(),
			result.GetBlob().GetUrl().GetFileSize())
		return
	}
	if *playlistID != "" {
		playlistStream, err := client.GetTracks(ctx, &enginelibrary.GetTracksRequest{
			LibraryId:  stringPtr("soundcloud-source"),
			PlaylistId: playlistID,
			PageSize:   uint32Ptr(200),
		})
		if err != nil {
			log.Fatal("GetTracks: ", err)
		}
		var count int
		for {
			response, err := playlistStream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				log.Fatal("GetTracks recv: ", err)
			}
			count += len(response.GetTracks())
		}
		fmt.Printf("playlist=%q tracks=%d\n", *playlistID, count)
		return
	}

	libraries, err := client.GetLibraries(ctx, &enginelibrary.GetLibrariesRequest{})
	if err != nil {
		log.Fatal("GetLibraries: ", err)
	}
	fmt.Printf("libraries=%d\n", len(libraries.GetLibraries()))
	for _, library := range libraries.GetLibraries() {
		fmt.Printf("library id=%q title=%q\n", library.GetId(), library.GetTitle())
	}

	library, err := client.GetLibrary(ctx, &enginelibrary.GetLibraryRequest{LibraryId: stringPtr("soundcloud-source")})
	if err != nil {
		log.Fatal("GetLibrary: ", err)
	}
	fmt.Printf("playlists=%d\n", len(library.GetPlaylists()))
	for _, playlist := range library.GetPlaylists() {
		printPlaylist(playlist, "root", 0)
	}

	tracksStream, err := client.GetTracks(ctx, &enginelibrary.GetTracksRequest{
		LibraryId:  stringPtr("soundcloud-source"),
		PlaylistId: stringPtr("likes-tracks"),
		PageSize:   uint32Ptr(5),
	})
	if err != nil {
		log.Fatal("GetTracks: ", err)
	}
	var tracks []*enginelibrary.ListTrack
	for {
		response, err := tracksStream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatal("GetTracks recv: ", err)
		}
		tracks = append(tracks, response.GetTracks()...)
		if len(tracks) >= 12 {
			break
		}
	}
	fmt.Printf("likes-tracks=%d\n", len(tracks))
	for i, track := range tracks {
		fmt.Printf("%d id=%q title=%q artist=%q artwork=%d\n", i,
			track.GetMetadata().GetId(), track.GetMetadata().GetTitle(),
			track.GetMetadata().GetArtist(), len(track.GetPreviewArtwork()))
		if i == 0 {
			result, err := client.GetTrack(ctx, &enginelibrary.GetTrackRequest{
				LibraryId: stringPtr("soundcloud-source"),
				TrackId:   track.GetMetadata().Id,
			})
			if err != nil {
				log.Fatal("GetTrack: ", err)
			}
			fmt.Printf("stream=%q size=%d\n", result.GetBlob().GetUrl().GetUrl(), result.GetBlob().GetUrl().GetFileSize())
		}
	}
}

func printPlaylist(playlist *enginelibrary.PlaylistMetadata, kind string, depth int) {
	indent := ""
	for range depth {
		indent += "  "
	}
	fmt.Printf("%s%s id=%q title=%q count=%d children=%d\n",
		indent, kind, playlist.GetId(), playlist.GetTitle(),
		playlist.GetTrackCount(), len(playlist.GetPlaylists()))
	for i, child := range playlist.GetPlaylists() {
		if i >= 20 {
			fmt.Printf("%s  ... %d more\n", indent, len(playlist.GetPlaylists())-i)
			break
		}
		printPlaylist(child, "child", depth+1)
	}
}

func stringPtr(value string) *string {
	return &value
}

func uint32Ptr(value uint32) *uint32 {
	return &value
}
