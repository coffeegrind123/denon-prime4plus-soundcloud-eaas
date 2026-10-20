#!/bin/sh
# Download an fMP4 HLS playlist with scfetch and concatenate init + segments
# into a local fragmented MP4. ffmpeg's network stack is broken on Engine OS
# 4.3.4 (SIGSEGV on any http/https input), so playback is local-file only.
#
# usage: hls-local.sh <playlist-url> [output.mp4]
set -u

HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
SCFETCH=${SCFETCH:-$HERE/bin/scfetch}
URL=${1:?usage: hls-local.sh <playlist-url> [output.mp4]}
OUT=${2:-/tmp/prime4-stream.mp4}
TMP=${TMPDIR:-/tmp}/hls.$$

rm -rf "$TMP"
mkdir -p "$TMP" || exit 1
trap 'rm -rf "$TMP"' EXIT INT TERM

"$SCFETCH" -q -O "$TMP/pl.m3u8" "$URL" || { echo "playlist fetch failed" >&2; exit 1; }

if grep -q '^#EXT-X-KEY' "$TMP/pl.m3u8"; then
    echo "encrypted HLS (EXT-X-KEY) is not supported" >&2
    exit 2
fi

INIT=$(grep '^#EXT-X-MAP' "$TMP/pl.m3u8" | sed 's/.*URI="\([^"]*\)".*/\1/' | head -n1)
grep '^https\?://' "$TMP/pl.m3u8" > "$TMP/segs.txt" || true
if [ -z "$INIT" ] && [ ! -s "$TMP/segs.txt" ]; then
    echo "no init/segments in playlist" >&2
    exit 1
fi

: > "$OUT"
if [ -n "$INIT" ]; then
    "$SCFETCH" -q -O "$TMP/init.mp4" "$INIT" || { echo "init fetch failed" >&2; exit 1; }
    cat "$TMP/init.mp4" >> "$OUT"
fi

i=0
while IFS= read -r seg; do
    [ -n "$seg" ] || continue
    i=$((i + 1))
    "$SCFETCH" -q -O "$TMP/seg" "$seg" || { echo "segment $i failed" >&2; exit 1; }
    cat "$TMP/seg" >> "$OUT"
    rm -f "$TMP/seg"
done < "$TMP/segs.txt"

echo "$OUT"
