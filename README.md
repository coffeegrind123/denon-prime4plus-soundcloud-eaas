# SoundCloud EaaS Source for Engine OS

This is an Engine OS source for a public SoundCloud profile. It runs on the
player itself and exposes that profile's tracks, likes, reposts, and public
playlists directly in the Engine UI.

It does not require SoundCloud account pairing, an Engine DJ Profile, Go+, or
Widevine. Only public, non-DRM streams are exposed.

Device and protocol research is documented separately in the
[Denon Prime 4+ research repository](https://github.com/coffeegrind123/denon-prime4plus-research).

## Verified target

- Denon DJ Prime 4+ (`JC11S`)
- Engine OS 4.3.4
- ARMv7 hard-float userland
- EaaS client enabled

Other Engine OS devices with an EaaS client and compatible ARM userland may
work, but are not verified.

## Requirements

### On the player

- Root access to install a service and write under `/data`
- ARMv7 build of the `soundcloud-eaas` binary
- Working systemd
- Free TCP ports `50010` and `50020`, plus UDP `11224`
- Outbound HTTPS access to SoundCloud and its media CDN
- Valid CA certificates
- Enough space for the stream cache; starts near zero and grows per track

The source does not need exclusive access to Engine's audio device. Engine
loads the HTTP stream through its normal EaaS playback path.

### Configuration

```text
SC_PROFILE=<public-profile-permalink>
SC_EAAS_HOST_IP=<player-or-source-lan-ip>
SC_MAX_FEED_ITEMS=200
```

`SC_PROFILE` is required. There is no default account.

When running directly on the player, use the player's LAN address for
`SC_EAAS_HOST_IP` so the EaaS discovery response advertises the correct gRPC
endpoint.

### Build host

- Go `1.27.1` or newer, or Docker for the optional container build
- An ARMv7 hard-float target for the player build

## Install on the player

Build a static ARMv7 binary:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
  go build -trimpath -ldflags="-s -w" \
  -o build/soundcloud-eaas-armv7 .
```

Create `/data/prime4-app/soundcloud-eaas.env` from
[`deploy/soundcloud-eaas.env.example`](deploy/soundcloud-eaas.env.example),
then install:

```sh
cp deploy/soundcloud-eaas.env.example deploy/soundcloud-eaas.env
$EDITOR deploy/soundcloud-eaas.env

ssh root@<player> 'mkdir -p /data/prime4-app/bin /data/prime4-app/cache'
scp build/soundcloud-eaas-armv7 root@<player>:/data/prime4-app/bin/soundcloud-eaas
scp deploy/soundcloud-eaas.env root@<player>:/data/prime4-app/soundcloud-eaas.env
scp deploy/soundcloud-eaas.service root@<player>:/etc/systemd/system/
ssh root@<player> 'chmod 755 /data/prime4-app/bin/soundcloud-eaas && \
  chmod 600 /data/prime4-app/soundcloud-eaas.env && \
  systemctl daemon-reload && \
  systemctl enable --now soundcloud-eaas'
```

If the source tile does not appear immediately, reopen the Source screen or
restart `engine.service`.

## Verify

```sh
ssh root@<player> 'systemctl status soundcloud-eaas --no-pager'
ssh root@<player> 'curl http://127.0.0.1:50020/ping'
```

The Engine source appears as `SoundCloud · <profile>` and exposes:

- `My Tracks`
- `Likes` → `Liked Tracks`, `Liked Playlists`
- `Reposts`
- `Playlists`

The first play of an HLS-only track may take several seconds while the source
downloads and concatenates the media segments. Repeated plays use the cache.

## How it works

```text
Engine UI
  │
  ├─ UDP 11224 discovery
  ├─ gRPC 50010 library and track metadata
  └─ HTTP 50020 audio and artwork
```

The source resolves public profile data through Soundcloak's anonymous
SoundCloud client. `GetLibrary` and `GetTracks` map the profile into Engine's
EaaS model. `GetTrack` returns a real file size, cue metadata, and a blob path
with the correct media extension.

Progressive tracks are cached and served locally. HLS tracks are materialized
from the initialization segment and media segments into a fragmented MP4.
HTTP `HEAD`, `GET`, and Range requests are supported.

## Docker

Docker is supported for development and for running the source on another LAN
host. It is not required when the binary runs directly on the player.

```sh
cp .env.example .env
$EDITOR .env
docker compose up -d --build
```

Set `SC_EAAS_HOST_IP` to the Docker host's LAN address, not the internal
container address.

## Project layout

```text
deploy/                  systemd unit and environment template
cmd/probe/               gRPC smoke tester
docs/                    architecture, API, and verification notes
legacy/                  earlier standalone resolver/player client
third_party/             pinned go-stagelinq and Soundcloak sources
main.go                  process setup, beacon, and gRPC/HTTP servers
source.go                Engine library and SoundCloud mappings
stream.go                audio preparation, caching, and HTTP range serving
```
