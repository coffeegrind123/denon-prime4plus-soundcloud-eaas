# SoundCloud EaaS Source

An Engine OS source for a public SoundCloud profile. It exposes the profile's
own tracks, likes, reposts, and public playlists directly in the hardware UI
without SoundCloud pairing, a Go+ subscription, or Widevine.

## How it works

- `11224/udp`: EAAS discovery request/response.
- `50010/tcp`: Engine's `NetworkTrustService` and `EngineLibraryService` gRPC API.
- `50020/tcp`: artwork and audio delivery.
- Soundcloak resolves the public SoundCloud API and non-DRM streams.
- HLS tracks are materialized into local fMP4 files for Engine's HTTP range reader.
- Likes and reposts are returned as one response; `SC_MAX_FEED_ITEMS` caps the recent feed (200 by default).

## Run

Set the public profile permalink and the Docker host's LAN address:

```sh
cp .env.example .env
$EDITOR .env
docker compose up -d --build
```

Required values:

```text
SC_PROFILE=<public-profile-permalink>
SC_EAAS_HOST_IP=<host-lan-ip>
```

The required ports are TCP 50010 and 50020, plus UDP 11224. Published ports
work better than `network_mode: host` on hosts that filter inbound host-network
UDP.

## Verify

```sh
curl http://HOST_IP:50020/ping
go run ./cmd/probe -address HOST_IP:50010
```

The Engine source appears as `SoundCloud · <profile>`. Selecting it exposes:

- `My Tracks`
- `Likes` -> `Liked Tracks`, `Liked Playlists`
- `Reposts`
- `Playlists`

Only streams that SoundCloud serves publicly without DRM are exposed.

## Layout

```text
cmd/probe/               gRPC smoke tester
docs/                    architecture, API, and test notes
legacy/                  earlier standalone resolver/player client
third_party/             pinned go-stagelinq and Soundcloak sources
main.go                  process setup, beacon, and gRPC/HTTP servers
source.go                Engine library and SoundCloud mappings
stream.go                progressive proxy and HLS materialization
```
