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

The source advertises the Docker host's LAN address rather than the internal
container address. Published Docker ports are therefore preferred:

```text
11224/udp
50010/tcp
50020/tcp
```

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
returns one response with up to `--max-feed-items` chronological entries.
`SearchTracks` uses the public SoundCloud search endpoint. `GetTrack` returns
an Engine path such as:

```text
<C:\SoundCloud\<track-id>>
```

The HTTP server parses the track ID from that path.

## Stream handling

Progressive streams are proxied with Range support. HLS streams are checked
for encryption, downloaded as init plus media segments, and concatenated into
one local fragmented MP4. Engine then reads the cache file through normal
HTTP `GET`/`HEAD`/Range semantics.

Only public, non-DRM transcodings are exposed. Encrypted HLS formats are
rejected rather than passed to Engine's Widevine path.

## Source refresh

The library tree is loaded at startup. No SoundCloud token is stored. The
public `client_id` is refreshed by Soundcloak's client initialization.
