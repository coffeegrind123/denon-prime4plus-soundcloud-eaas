# Testing

## Build and unit tests

```sh
docker build -t denon-prime4plus-soundcloud-eaas:test .
```

The Docker build runs `go test ./...` and `go vet ./...` before compiling the
runtime image.

## Container

```sh
cp .env.example .env
# Set SC_EAAS_HOST_IP to the Docker host's LAN address.
docker compose up -d --build
```

## Discovery

The discovery reply begins with:

```text
45 41 41 53 01 01
```

and advertises the configured LAN address:

```text
grpc://<SC_EAAS_HOST_IP>:50010
```

For an on-player deployment, `SC_EAAS_HOST_IP` is the secondary source alias,
not Engine's primary LAN address.

## gRPC

```sh
docker run --rm --network host \
  -v "$PWD:/src" -w /src \
  -e GOTOOLCHAIN=auto \
  golang:1.24-alpine \
  sh -lc 'apk add --no-cache git ca-certificates >/dev/null && \
          go run ./cmd/probe -address HOST:50010'
```

Expected tree:

```text
My Tracks
Likes
Reposts
Playlists
```

## Audio

```sh
curl -D - -H 'Range: bytes=0-1023' \
  'http://HOST:50020/download/%3CC%3A%5CSoundCloud%5C<track-id>.m4a%3E'
```

Expected:

```text
HTTP/1.1 206 Partial Content
Accept-Ranges: bytes
Content-Type: audio/mp4
Content-Range: bytes 0-1023/<size>
```

## Engine verification

On Engine OS 4.3.4 the source appears as `SoundCloud · <profile>`.
Likes and reposts render with artwork, title, artist, and duration. The feed
caps at `SC_MAX_FEED_ITEMS` (200 by default) and the UI scrolls through that
set.

From the unit itself, the HTTP and boot checks can be run with BusyBox:

```sh
wget -qO- http://<source-alias>:50020/ping
tail -20 /data/prime4-app/boot-start.log
systemctl is-active soundcloud-eaas engine.service
```

## UTF-8 and size limits

All SoundCloud-supplied strings are normalized with
`strings.ToValidUTF8` before protobuf marshaling. The first 50 list entries
carry embedded artwork; later entries omit artwork to keep the single response
bounded.
