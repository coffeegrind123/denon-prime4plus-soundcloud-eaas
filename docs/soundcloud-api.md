# Public SoundCloud API research

The source uses an anonymous SoundCloud `client_id`. This is a public
application identifier, not a user OAuth token.

## Profile

```text
GET /resolve?url=https%3A%2F%2Fsoundcloud.com%2F<profile>&client_id=...
GET /users/<id>?client_id=...
```

## Tracks and playlists

```text
GET /users/<id>/tracks
GET /users/<id>/playlists_without_albums
GET /playlists/<id>
GET /tracks?ids=<up-to-50-ids>
```

Playlist responses may contain full tracks for the first few entries and ID-only
stubs for the remainder. Stubs must be filled using the batch `/tracks?ids=`
endpoint and matched back by ID, not by response order.

## Likes and reposts

```text
GET /users/<id>/likes
GET /stream/users/<id>/reposts
```

Pagination is opaque. Follow `next_href`; do not synthesize offsets. Likes
contain either `.track` or `.playlist`.

## Streams

```text
GET /tracks/<id>
GET <media.transcodings[].url>?client_id=...&track_authorization=...
GET <signed stream URL>
```

Relevant protocols:

| Protocol | Use |
|---|---|
| `progressive` | Materialize into the bounded local cache |
| `hls` | Materialize init + segments into fMP4 |
| `ctr-encrypted-hls` | Reject; protected content |
| `cbc-encrypted-hls` | Reject; protected content |

`policy: "MONETIZE"` does not by itself indicate DRM. Check the actual
transcoding protocol.

## Scope

The source intentionally ignores private content, account feeds, Go+ content,
Widevine, and FairPlay. Public/free streams are the supported boundary.
