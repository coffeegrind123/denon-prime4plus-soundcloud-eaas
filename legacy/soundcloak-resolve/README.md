# soundcloak-resolve

Small CLI that links directly against Soundcloak's `lib/sc` and selects a
non-DRM stream (progressive MP3 first, then HLS).

## License

This directory links against **Soundcloak**, which is licensed under the GNU
Affero General Public License v3.0. The resulting binary and this directory are
therefore AGPL-3.0, unlike the MIT-licensed rest of this repository.

## Build

The Soundcloak module is not vendored. Clone it next to this repository:

```sh
git clone https://git.maid.zone/stuff/soundcloak ../soundcloak
cd client/soundcloak-resolve
go mod tidy
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 \
  go build -trimpath -ldflags '-s -w' -o ../../build/soundcloak-resolve .
```

The `go.mod` `replace` points at `../soundcloak`; adjust it if the clone lives
elsewhere.

## Usage

```sh
soundcloak-resolve https://soundcloud.com/<user>/<track>
```

Output:

```json
{"title":"...","policy":"...","protocol":"hls","preset":"...","quality":"...","url":"..."}
```
