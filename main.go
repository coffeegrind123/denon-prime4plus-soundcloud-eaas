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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

const (
	softwareName = "SoundCloud EaaS"
	softwareVer  = "0.2.7"
)

func main() {
	var (
		profile  string
		hostIP   string
		localIP  string
		cacheDir string
		display  string
		grpcPort int
		httpPort int
		feedMax  int
		cacheMax int64
		cacheMin int64
	)

	flag.StringVar(&profile, "user", "", "SoundCloud profile permalink (required)")
	flag.StringVar(&hostIP, "host-ip", "", "IP address Engine should use for HTTP streams")
	flag.StringVar(&localIP, "local-ip", "", "Local IPv4 address for source listeners and discovery replies")
	flag.StringVar(&cacheDir, "cache", "/data/prime4-app/cache", "Stream cache directory")
	flag.StringVar(&display, "name", "", "Source name shown in Engine")
	flag.IntVar(&grpcPort, "grpc-port", int(eaas.DefaultEAASGRPCPort), "EAAS gRPC port")
	flag.IntVar(&httpPort, "http-port", int(eaas.DefaultEAASHTTPPort), "EAAS HTTP port")
	flag.IntVar(&feedMax, "max-feed-items", 200, "Maximum items returned for chronological feeds")
	flag.Int64Var(&cacheMax, "cache-max-bytes", defaultCacheMaxBytes, "Maximum bytes used by the stream cache")
	flag.Int64Var(&cacheMin, "cache-min-free-bytes", minimumCacheFreeBytes, "Minimum free bytes to leave on the cache filesystem")
	flag.Parse()

	if profile == "" {
		log.Fatal("--user is required")
	}
	if cacheMin < minimumCacheFreeBytes {
		log.Printf("raising cache free-space floor from %d to %d bytes", cacheMin, minimumCacheFreeBytes)
		cacheMin = minimumCacheFreeBytes
	}
	if display == "" {
		display = "SoundCloud · " + profile
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		log.Fatalf("create cache directory: %v", err)
	}

	source, err := newSource(profile, display, hostIP, cacheDir, feedMax, cacheMax, cacheMin)
	if err != nil {
		log.Fatalf("load SoundCloud profile: %v", err)
	}
	log.Printf("loaded SoundCloud profile %s: %d tracks, %d likes, %d playlists",
		source.user.Permalink, source.user.Tracks, source.user.Liked, source.user.Playlists)
	log.Printf("cache directory=%s max=%d bytes min-free=%d bytes", cacheDir, cacheMax, cacheMin)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()

	grpcOptions := []grpc.ServerOption{grpc.MaxSendMsgSize(32 * 1024 * 1024)}
	httpHandler := source.httpHandler()
	if localIP != "" {
		localPeers, err := localPeerIPs()
		if err != nil {
			log.Fatalf("enumerate local addresses: %v", err)
		}
		grpcOptions = append(
			grpcOptions,
			grpc.UnaryInterceptor(localOnlyUnary(localPeers)),
			grpc.StreamInterceptor(localOnlyStream(localPeers)),
		)
		httpHandler = localOnlyHTTP(httpHandler, localPeers)
	}
	grpcServer := grpc.NewServer(grpcOptions...)
	enginelibrary.RegisterEngineLibraryServiceServer(grpcServer, source)
	networktrust.RegisterNetworkTrustServiceServer(grpcServer, &trustServer{})

	grpcListener, err := net.Listen("tcp", net.JoinHostPort(localIP, fmt.Sprint(grpcPort)))
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
		Addr:              net.JoinHostPort(localIP, fmt.Sprint(httpPort)),
		Handler:           httpHandler,
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
		SourceHost:      localIP,
		GRPCPort:        uint16(grpcPort),
	})
	if err != nil {
		log.Fatalf("start EAAS beacon: %v", err)
	}

	log.Printf("EAAS source ready: UDP 11224, gRPC %d, HTTP %d", grpcPort, httpPort)
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

func localPeerIPs() (map[string]struct{}, error) {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	allowed := map[string]struct{}{"127.0.0.1": {}}
	for _, address := range addresses {
		host, _, err := net.ParseCIDR(address.String())
		if err == nil {
			allowed[host.String()] = struct{}{}
		}
	}
	return allowed, nil
}

func allowedPeer(address net.Addr, allowed map[string]struct{}) bool {
	if address == nil {
		return false
	}
	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		return false
	}
	_, ok := allowed[host]
	return ok
}

func localOnlyUnary(allowed map[string]struct{}) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		remote, _ := peer.FromContext(ctx)
		if remote == nil || !allowedPeer(remote.Addr, allowed) {
			return nil, status.Error(codes.PermissionDenied, "local clients only")
		}
		return handler(ctx, req)
	}
}

func localOnlyStream(allowed map[string]struct{}) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		remote, _ := peer.FromContext(stream.Context())
		if remote == nil || !allowedPeer(remote.Addr, allowed) {
			return status.Error(codes.PermissionDenied, "local clients only")
		}
		return handler(srv, stream)
	}
}

func localOnlyHTTP(next http.Handler, allowed map[string]struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "local clients only", http.StatusForbidden)
			return
		}
		if _, ok := allowed[host]; !ok {
			http.Error(w, "local clients only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
