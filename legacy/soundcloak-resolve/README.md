# soundcloak-resolve

Small CLI that links directly against Soundcloak's `lib/sc` and selects a
non-DRM stream in this order: progressive, MPEG restream, then MPEG HLS.

## License

Project-owned code uses the repository's MIT license. This CLI links against
the vendored **Soundcloak** tree at `third_party/soundcloak`, which is licensed
under AGPL-3.0. See `THIRD_PARTY.md`.

## Build

The module uses the vendored Soundcloak tree:

```sh
cd legacy/soundcloak-resolve
go mod tidy
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 \
  go build -trimpath -ldflags '-s -w' -o ../../build/soundcloak-resolve .
```

The checked-in `go.mod` `replace` points at `../../third_party/soundcloak`.

## Usage

```sh
soundcloak-resolve <soundcloud-url|user/track|id>
```

Output:

```json
{"title":"...","policy":"...","protocol":"hls","preset":"...","quality":"...","url":"..."}
```
