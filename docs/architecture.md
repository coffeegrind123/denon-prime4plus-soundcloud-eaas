# Architecture

## Engine integration

Engine OS implements third-party sources through EaaS:

```text
UDP 11224 request/response discovery
        |
        v
grpc://HOST:50010
        |
        +-- networktrust.v1.NetworkTrustService
        +-- enginelibrary.v1.EngineLibraryService
        |
        v
HTTP 50020 /download/<path>
```

Discovery is a request/response protocol. The player sends `EAAS 01 00` to
UDP 11224 and the source replies with `EAAS 01 01`, a 16-byte token, hostname,
gRPC URL, software version, and a protocol byte.

For Docker, the source advertises the host's LAN address rather than the
internal container address:

```text
11224/udp
50010/tcp
50020/tcp
```

For an on-player deployment, Engine already owns TCP 50010 at its primary LAN
address and at `127.0.0.1`. The source therefore:

1. assigns a secondary address such as `192.168.4.90/32` to the LAN interface;
2. binds gRPC/HTTP only to that address;
3. advertises the same address over discovery;
4. accepts gRPC/HTTP only from local interface addresses.

The standard 50010/50020 ports are required for reliable UI activation. UDP
11224 is shared with Engine through `SO_REUSEPORT`.

## Library mapping

The public profile becomes one EaaS library:

```text
My Tracks
Likes
  Liked Tracks
  Liked Playlists
    <public playlists>
Reposts
Playlists
  <public playlists>
```

`GetLibraries` exposes the source. `GetLibrary` exposes the tree. `GetTracks`
returns one response with up to `--max-feed-items` chronological entries,
capped at 200.
`SearchTracks` uses the public SoundCloud search endpoint. `GetTrack` returns
an Engine path such as:

```text
<C:\SoundCloud\<track-id>.<extension>>
```

The HTTP server parses the track ID from that path.

## Stream handling

Progressive and HLS streams are materialized into bounded local media files.
HLS is checked for unsupported master-playlist, encryption, and byte-range
forms, then downloaded as init plus media segments into one fragmented MP4.
Engine reads the open file through `http.ServeContent`, so eviction cannot
remove the file underneath an active Range response.

The cache has a 512 MiB default quota, a 1 GiB free-space floor, a 2 GiB hard
cap, and oldest-completed-first eviction. Each response length reserves budget
before copying; free space is checked during the copy. Partial files are
removed on errors or startup.

Engine applies a short deadline to `GetTrack`. For HLS, `GetTrack` now fetches
the playlist, resolves every segment length with concurrent `HEAD` requests,
returns the exact total size, and starts materialization in the background.
Segment bodies download in bounded parallel batches. If no cache exists when
HTTP `/download` arrives, the handler waits for that materialization. Once a
slot is acquired, copying also uses a separate 15-minute bounded context so it
cannot inherit a canceled RPC.

Only public, non-DRM transcodings are exposed. Encrypted HLS formats are
rejected rather than passed to Engine's Widevine path.

## Source refresh

The library tree is loaded at startup. No SoundCloud token is stored. The
public `client_id` is refreshed by Soundcloak's client initialization.

The firmware does not pick up newly added `/etc` systemd wants/drop-ins in the
boot transaction. `deploy/boot-start.sh` is invoked from the persistent
`/data/payload/activate.sh` hook after a route and outbound connectivity are
available, then starts the enabled service.
