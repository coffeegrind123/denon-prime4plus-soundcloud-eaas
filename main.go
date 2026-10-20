package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/icedream/go-stagelinq/eaas"
	"github.com/icedream/go-stagelinq/eaas/proto/enginelibrary"
	"github.com/icedream/go-stagelinq/eaas/proto/networktrust"
	"google.golang.org/grpc"
)

const (
	softwareName = "SoundCloud EaaS"
	softwareVer  = "0.1.0"
)

func main() {
	var (
		profile  string
		hostIP   string
		cacheDir string
		display  string
		grpcPort int
		httpPort int
		feedMax  int
	)

	flag.StringVar(&profile, "user", "", "SoundCloud profile permalink (required)")
	flag.StringVar(&hostIP, "host-ip", "", "IP address Engine should use for HTTP streams")
	flag.StringVar(&cacheDir, "cache", "/data/soundcloud-eaas", "Stream cache directory")
	flag.StringVar(&display, "name", "", "Source name shown in Engine")
	flag.IntVar(&grpcPort, "grpc-port", int(eaas.DefaultEAASGRPCPort), "EAAS gRPC port")
	flag.IntVar(&httpPort, "http-port", int(eaas.DefaultEAASHTTPPort), "EAAS HTTP port")
	flag.IntVar(&feedMax, "max-feed-items", 200, "Maximum items returned for chronological feeds")
	flag.Parse()

	if profile == "" {
		log.Fatal("--user is required")
	}
	if display == "" {
		display = "SoundCloud · " + profile
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		log.Fatalf("create cache directory: %v", err)
	}

	source, err := newSource(profile, display, hostIP, cacheDir, feedMax)
	if err != nil {
		log.Fatalf("load SoundCloud profile: %v", err)
	}
	log.Printf("loaded SoundCloud profile %s: %d tracks, %d likes, %d playlists",
		source.user.Permalink, source.user.Tracks, source.user.Liked, source.user.Playlists)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()

	grpcServer := grpc.NewServer(grpc.MaxSendMsgSize(32 * 1024 * 1024))
	enginelibrary.RegisterEngineLibraryServiceServer(grpcServer, source)
	networktrust.RegisterNetworkTrustServiceServer(grpcServer, &trustServer{})

	grpcListener, err := net.Listen("tcp", fmt.Sprintf(":%d", grpcPort))
	if err != nil {
		log.Fatalf("listen gRPC: %v", err)
	}
	go func() {
		if err := grpcServer.Serve(grpcListener); err != nil {
			log.Printf("gRPC server: %v", err)
			cancel()
		}
	}()

	httpServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", httpPort),
		Handler:           source.httpHandler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server: %v", err)
			cancel()
		}
	}()

	token := eaas.Token{
		0x53, 0x43, 0x2d, 0x45, 0x41, 0x41, 0x53, 0x2d,
		0x6b, 0x75, 0x73, 0x6f, 0x70, 0x61, 0x73, 0x6b,
	}
	beacon, err := eaas.StartBeaconWithConfiguration(&eaas.BeaconConfiguration{
		Context:         ctx,
		Name:            display,
		SoftwareVersion: softwareVer,
		Token:           token,
		GRPCHost:        hostIP,
		GRPCPort:        uint16(grpcPort),
	})
	if err != nil {
		log.Fatalf("start EAAS beacon: %v", err)
	}

	log.Printf("EAAS source ready: UDP %d, gRPC %d, HTTP %d", 11224, grpcPort, httpPort)
	<-ctx.Done()
	log.Printf("shutting down")
	_ = beacon.Shutdown()
	grpcServer.Stop()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)
}

// trustServer grants every request, including Engine's initial network-trust
// handshake. This source is intended for a trusted local DJ LAN.
type trustServer struct {
	networktrust.UnimplementedNetworkTrustServiceServer
}

func (s *trustServer) CreateTrust(_ context.Context, req *networktrust.CreateTrustRequest) (*networktrust.CreateTrustResponse, error) {
	log.Printf("CreateTrust device=%q key=%q wireguard_port=%d",
		req.GetDeviceName(), shortKey(req.GetEd25519Pk()), req.GetWireguardPort())
	return &networktrust.CreateTrustResponse{
		Response: &networktrust.CreateTrustResponse_Granted{
			Granted: &networktrust.CreateTrustGranted{},
		},
	}, nil
}

func shortKey(key string) string {
	if len(key) <= 16 {
		return key
	}
	return key[:16] + "…"
}
