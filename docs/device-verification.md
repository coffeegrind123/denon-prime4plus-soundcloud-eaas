# Device verification

Verified against Engine OS 4.3.4 on a Denon Prime 4+.

## Discovery

The source replied to `EAAS 01 00` over UDP 11224 with a 120-byte
`EAAS 01 01` response advertising:

```text
grpc://<HOST_LAN_IP>:50010
```

Publishing UDP 11224 and TCP 50010/50020 through Docker worked; host-network
UDP was blocked by the development host's firewall.

## On-player deployment

The source was also run directly on the player:

```text
source alias: 192.168.4.90/32 on wlan0
gRPC:         192.168.4.90:50010
HTTP:         192.168.4.90:50020
cache:        /data/prime4-app/cache
binary:       static ARMv7
service user: soundcloud-eaas
```

The reply packet source address was set to the alias. Engine then completed
`CreateTrust`, `GetLibrary`, and `GetTracks`. An alias on `lo` and alternate
gRPC ports 50110/50120 appeared in the picker but failed activation.

The UI loaded `Likes`, displayed artwork and metadata, and double-tapping a row
opened the Load Track deck selector. Loading the first track caused `GetTrack`
and a local `/download` request without an Engine format error.

The cache contained one numeric fragmented MP4 file after the first load.
`/data` remained at 5.4 GiB free. The HTTP endpoint rejected a non-local test
client with 403.

The service persisted across boot and was started by the `/data` boot hook after
network readiness. The binary, service unit, and boot helper hashes matched a
fresh ARMv7 build from the repository.

## Library

The hardware source list displayed:

```text
SOUNDCLOUD · <PROFILE>
```

Selecting it displayed:

```text
My Tracks
Likes
Reposts
Playlists
```

`Likes` displayed artwork, title, artist, and duration. `Reposts` loaded a
200-entry feed without an Engine error. `Playlists` listed all public
playlists.

## Metadata

Profile data observed through the public API:

```text
tracks=<configured profile track count>
likes=<configured profile like count>
playlists=<configured profile playlist count>
```

The live list response contained embedded artwork for the first 50 items and
metadata only after that, keeping the protobuf response bounded.

## Stream

Resolved public HLS track:

```text
track=<track-id>
protocol=hls
preset=aac_160k
cache=/var/cache/soundcloud-eaas/<track-id>.m4a
size=8949952
```

HTTP request:

```text
GET /download/<C:\SoundCloud\<track-id>>
Range: bytes=0-1023
```

Response:

```text
206 Partial Content
Content-Length: 1024
Content-Range: bytes 0-1023/8949952
Content-Type: audio/mp4
Accept-Ranges: bytes
```

The first bytes are a valid fragmented MP4 (`ftyp iso5`, followed by `moov`).

## Issues found and fixed

* Engine rejected blob paths ending in a numeric ID with `eassapi: Invalid
  Track format`. The path must include the resolved media extension, for
  example `<C:\SoundCloud\<track-id>.m4a>`.
* A zero `TrackBlobUrl.file_size` is not accepted. `GetTrack` now resolves or
  materializes the stream first and returns its actual size.
* The performance-data message needs `MainCue` coordinates. Unset positions are
  sent as `-1`.
* Docker host networking allowed TCP but not inbound UDP; published ports
  fixed discovery.
* The stock `go-stagelinq` generated service did not match Engine's embedded
  server-streaming descriptors; the local protobuf service was regenerated.
* Multiple gRPC responses confused Engine; the source sends one populated
  response.
* One public feed contained invalid UTF-8. All external strings are normalized
  before protobuf marshaling.
* Large feed responses exceeded practical client limits. Only the first 50
  rows carry artwork.
