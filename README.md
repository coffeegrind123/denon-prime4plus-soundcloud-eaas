# SoundCloud EaaS Source for Engine OS

This is an Engine OS source for a public SoundCloud profile. It runs on the
player itself and exposes that profile's tracks, likes, reposts, and public
playlists directly in the Engine UI.

It does not require SoundCloud account pairing, an Engine DJ Profile, Go+, or
Widevine. Only public, non-DRM streams are exposed.

Device and protocol research is documented separately in the
[Denon Prime 4+ research repository](https://github.com/coffeegrind123/denon-prime4plus-research).

## Verified target

- Source release `v0.2.6`
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
- Free TCP ports `50010` and `50020` on the source address, plus shared UDP `11224`
- Outbound HTTPS access to SoundCloud and its media CDN
- Valid CA certificates
- At least 1 GiB free under the cache filesystem

The source does not need exclusive access to Engine's audio device. Engine
loads the HTTP stream through its normal EaaS playback path.

### Configuration

```text
SC_PROFILE=<public-profile-permalink>
SC_EAAS_HOST_IP=<advertised-eaas-ip>
SC_LOCAL_IP=<local-source-alias>
SC_LOCAL_IF=wlan0
SC_MAX_FEED_ITEMS=200
SC_GRPC_PORT=50010
SC_HTTP_PORT=50020
SC_CACHE_MAX_BYTES=536870912
SC_CACHE_MIN_FREE_BYTES=1073741824
```

`SC_PROFILE` is required. There is no default account.

When running directly on the player, set both `SC_EAAS_HOST_IP` and
`SC_LOCAL_IP` to a dedicated source alias such as `192.168.4.90`. This keeps
the beacon reply on a distinct EaaS address while Engine retains its primary
LAN address. `SC_LOCAL_IF` names the LAN interface that owns the alias.
The systemd unit creates the address before startup. Engine already owns TCP
`50010` on the primary LAN address and `127.0.0.1`; the source binds the same
standard ports only on the alias. UDP `11224` is shared through `SO_REUSEPORT`.

The cache quota is 512 MiB by default and cannot be configured above 2 GiB.
The free-space floor is 1 GiB and cannot be configured lower.

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
scp deploy/boot-start.sh root@<player>:/data/prime4-app/boot-start.sh
scp deploy/soundcloud-eaas.service root@<player>:/etc/systemd/system/
ssh root@<player> 'if ! grep -q "^soundcloud-eaas:" /etc/group; then \
    addgroup -S soundcloud-eaas; \
  fi && \
  if ! grep -q "^soundcloud-eaas:" /etc/passwd; then \
    adduser -S -D -H -G soundcloud-eaas \
      -s /sbin/nologin -h /nonexistent soundcloud-eaas; \
  fi && \
  chown -R soundcloud-eaas:soundcloud-eaas /data/prime4-app/cache && \
  chmod 755 /data/prime4-app/bin/soundcloud-eaas && \
  chmod 755 /data/prime4-app/boot-start.sh && \
  chmod 600 /data/prime4-app/soundcloud-eaas.env && \
  systemctl daemon-reload && \
  systemctl enable --now soundcloud-eaas'
```

This firmware does not activate new `multi-user.target.wants` entries from the
late `/etc` overlay at boot. Add this idempotent hook to the existing
`/data/payload/activate.sh`:

```sh
if [ -x /data/prime4-app/boot-start.sh ]; then
    /data/prime4-app/boot-start.sh >>/data/prime4-app/boot-start.log 2>&1 &
fi
```

Insert that block immediately before the final `exit 0` in `activate.sh`.
The hook runs from `/data` after boot, waits for a default route plus outbound
connectivity, then starts the enabled service. This avoids adding the source
alias while Wi-Fi is still completing DHCP. `/run` guards it to one attempt per
boot.

If the source tile does not appear immediately, reopen the Source screen or
restart `engine.service`.

## Verify

```sh
ssh root@<player> 'systemctl status soundcloud-eaas --no-pager'
ssh root@<player> 'wget -qO- http://<source-alias>:50020/ping'
```

The Engine source appears as `SoundCloud · <profile>` and exposes:

- `My Tracks`
- `Likes` → `Liked Tracks`, `Liked Playlists`
- `Reposts`
- `Playlists`

The first play of an HLS-only track may take several seconds while the source
downloads and concatenates the media segments. Repeated plays use the cache.

## Stream cache

The cache starts empty. Each track is added only when Engine prepares or plays
it, so disk use grows one media file at a time. A typical 128 kbit/s track uses
roughly 5 MiB per five minutes. The default 512 MiB quota therefore holds
roughly 100 tracks, depending on their length and bitrate.

Cache files are media only. The directory does not contain SoundCloud
credentials, cookies, or account tokens. It is safe to stop the service and
delete `/data/prime4-app/cache`; the next play will recreate the needed file.

The source enforces three independent limits:

- It writes no more than the configured quota in total: 512 MiB by default,
  with a 2 GiB hard maximum.
- It stops if writing would leave less than 1 GiB free on the filesystem.
- It deletes the oldest completed track files first when either limit is
  reached.

Only numeric track files with `.m4a`, `.mp3`, `.mp4`, or `.aac` extensions are
managed. Unrelated files in the cache directory are never deleted. Interrupted
`.part` downloads are removed at startup.

Before a download starts, the source calculates a hard byte budget from both
the quota and current free space. It also checks free space while copying and
aborts the download before crossing the floor. Partial files are removed on
abort. Downloads are serialized, so concurrent Engine requests cannot each
consume the full quota.

If the quota cannot be satisfied even after eviction, the track is reported as
unavailable rather than continuing to write. If the filesystem is already
below the 1 GiB floor at startup, the service exits and enters its systemd
restart limit. Engine remains usable; the storage safety check fails closed
instead of filling `/data`.

## How it works

```text
Engine UI
  │
  ├─ UDP 11224 discovery
  ├─ gRPC 50010 library, track metadata, and embedded artwork
  └─ HTTP 50020 cached audio
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
container address. Leave `SC_LOCAL_IP` empty for Docker: the local-peer filter
is intentionally on-player-only. Firewall TCP 50010/50020 and UDP 11224 to the
intended DJ LAN.

## Project layout

```text
deploy/                  systemd unit and environment template
cmd/probe/               gRPC smoke tester
cmd/discover/            UDP discovery smoke tester
docs/                    architecture, API, and verification notes
legacy/                  earlier standalone resolver/player client
third_party/             pinned go-stagelinq and Soundcloak sources
main.go                  process setup, beacon, and gRPC/HTTP servers
source.go                Engine library and SoundCloud mappings
stream.go                audio preparation, caching, and HTTP range serving
```
