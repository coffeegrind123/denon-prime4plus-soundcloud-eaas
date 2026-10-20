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
