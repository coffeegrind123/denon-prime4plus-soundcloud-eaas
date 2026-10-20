#!/bin/sh
# Resolve a SoundCloud track with Soundcloak's lib/sc, download the stream with
# scfetch, and play it on ALSA locally.
#
# ffmpeg's network stack SIGSEGVs on Engine OS 4.3.4 for any http/https input,
# so HLS is fetched by scfetch and concatenated into a local fMP4 first.
set -u

HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
RESOLVER=${SC_RESOLVER:-$HERE/bin/soundcloak-resolve}
JQ=${SC_JQ:-$HERE/bin/jq}
FFMPEG=${SC_FFMPEG:-$HERE/bin/ffmpeg}
SCFETCH=${SCFETCH:-$HERE/bin/scfetch}
HLS=${SC_HLS:-$HERE/hls-local.sh}
AUDIO=${SC_AUDIO_DEV:-plughw:0,0}
TRACK=${1:?usage: play-soundcloak.sh <soundcloud-url|permaink>}

JSON=$("$RESOLVER" "$TRACK" 2>/dev/null) || { echo "resolve failed" >&2; exit 1; }
URL=$(printf '%s' "$JSON" | "$JQ" -r .url)
TITLE=$(printf '%s' "$JSON" | "$JQ" -r .title)
[ -n "$URL" ] && [ "$URL" != null ] || { echo "no stream URL" >&2; exit 1; }

echo "track: $TITLE" >&2

case "$URL" in
    *.m3u8*)
        FILE=$("$HLS" "$URL" /tmp/prime4-stream.mp4) || exit 1
        ;;
    *)
        FILE=/tmp/prime4-stream.bin
        "$SCFETCH" -q -O "$FILE" "$URL" || exit 1
        ;;
esac

echo "playing: $FILE" >&2
"$FFMPEG" -hide_banner -loglevel warning -i "$FILE" -f alsa "$AUDIO"
