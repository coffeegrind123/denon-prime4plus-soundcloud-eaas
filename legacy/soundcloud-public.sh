#!/bin/sh
# Public/free SoundCloud player for Engine OS 4.3.4 (ARMv7).
#
# It uses SoundCloud's public api-v2 client_id flow, selects an unencrypted
# HLS/MP3 transcoding, and plays it through ALSA with a static ffmpeg.
# It deliberately does not attempt Widevine/DRM or Go+-gated tracks.
#
# Usage:
#   soundcloud-public.sh <soundcloud-track-url> [...]
#   soundcloud-public.sh --user <permalink>
#   soundcloud-public.sh --feed <permalink>
#   soundcloud-public.sh --me                 # needs SC_OAUTH_TOKEN
#   soundcloud-public.sh --select-stream < track.json
#
# Environment:
#   SC_CLIENT_ID       optional 32-char SoundCloud client_id
#   SC_OAUTH_TOKEN     optional OAuth token for --me
#   SC_AUDIO_DEV       ALSA device (default: default)
#   SC_JQ / SC_FFMPEG / SC_WGET  override binaries (host-side tests)

set -u

HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
JQ=${SC_JQ:-"$HERE/bin/jq"}
FFMPEG=${SC_FFMPEG:-"$HERE/bin/ffmpeg"}
WGET=${SC_WGET:-wget}
AUDIO_DEV=${SC_AUDIO_DEV:-default}
CLIENT_ID=${SC_CLIENT_ID:-}
OAUTH_TOKEN=${SC_OAUTH_TOKEN:-}
UA='Mozilla/5.0 (X11; Linux armv7l) AppleWebKit/537.36'
LOG=${SC_LOG:-/data/soundcloud-public.log}
TMP=${TMPDIR:-/tmp}/soundcloud-public.$$

command -v "$JQ" >/dev/null 2>&1 || JQ=jq

log() {
    line="$(date -u +%FT%TZ 2>/dev/null || date) $*"
    echo "$line" >&2
    { echo "$line" >>"$LOG"; } 2>/dev/null || true
}

die() {
    log "ERROR: $*"
    exit 1
}

cleanup() {
    rm -rf "$TMP" 2>/dev/null || true
}
trap cleanup EXIT INT TERM
mkdir -p "$TMP" || die "cannot create $TMP"

fetch() {
    _fetch_url=$1
    _fetch_out=$2
    if ! "$WGET" -q -O "$_fetch_out" -U "$UA" \
        --header='Accept-Encoding: identity' "$_fetch_url"; then
        return 1
    fi
    return 0
}

json_get() {
    _json_file=$1
    _json_filter=$2
    "$JQ" -r "$_json_filter" "$_json_file" 2>/dev/null
}

get_client_id() {
    [ -n "$CLIENT_ID" ] && return 0
    _client_page="$TMP/soundcloud.html"
    fetch 'https://soundcloud.com/' "$_client_page" || die "cannot fetch soundcloud.com"
    CLIENT_ID=$(
        grep -oE 'client_id:"[A-Za-z0-9]{32}"' "$_client_page" 2>/dev/null |
        head -1 | cut -d'"' -f2
    )
    [ -n "$CLIENT_ID" ] || CLIENT_ID=$(
        grep -oE '"apiClient","data":\{"id":"[A-Za-z0-9]{32}"' "$_client_page" 2>/dev/null |
        head -1 | grep -oE '[A-Za-z0-9]{32}'
    )
    [ -n "$CLIENT_ID" ] || die "could not extract SoundCloud client_id"
    log "using client_id ${CLIENT_ID%????????????????????????}"
}

urlencode() {
    printf '%s' "$1" | "$JQ" -Rr @uri
}

# Prints: "<protocol>\t<url>" for a playable unencrypted transcoding, or
# "drm" if the only transcodings are encrypted.
select_stream() {
    _stream_file=$1
    _stream_line=$(
        "$JQ" -r '
          [.media.transcodings[]? |
            select(.format.protocol == "progressive" and
                   .format.mime_type == "audio/mpeg")]
          | .[0] | if . == null then empty
            else "progressive\t" + .url end
        ' "$_stream_file" 2>/dev/null
    )
    if [ -z "$_stream_line" ]; then
        _stream_line=$(
            "$JQ" -r '
              [.media.transcodings[]? |
                select(.format.protocol == "hls")]
              | .[0] | if . == null then empty
                else "hls\t" + .url end
            ' "$_stream_file" 2>/dev/null
        )
    fi
    if [ -z "$_stream_line" ]; then
        if "$JQ" -e '
            [.media.transcodings[]? |
              select(.format.protocol == "ctr-encrypted-hls" or
                     .format.protocol == "cbc-encrypted-hls")]
            | length > 0
        ' "$_stream_file" >/dev/null 2>&1; then
            _stream_line="drm"
        fi
    fi
    printf '%s\n' "$_stream_line"
}

stream_for_track() {
    _track_file=$1
    _stream_line=$(select_stream "$_track_file")
    [ -n "$_stream_line" ] || return 1
    if [ "$_stream_line" = "drm" ]; then
        log "DRM/Go+ required: no unencrypted HLS/progressive transcoding"
        return 2
    fi

    _stream_proto=${_stream_line%%	*}
    _stream_turl=${_stream_line#*	}
    _track_auth=$(json_get "$_track_file" '.track_authorization // empty')
    _stream_out="$TMP/stream.json"
    fetch "$_stream_turl?client_id=$CLIENT_ID&track_authorization=$(urlencode "$_track_auth")" "$_stream_out" ||
        { log "stream endpoint failed"; return 1; }
    _stream_url=$(json_get "$_stream_out" '.url // empty')
    [ -n "$_stream_url" ] || { log "stream response has no url"; return 1; }
    printf '%s\n' "$_stream_url"
}

play_track_url() {
    _track_url=$1
    get_client_id
    _track_enc=$(urlencode "$_track_url")
    _track_json="$TMP/track.json"
    fetch "https://api-v2.soundcloud.com/resolve?url=$_track_enc&client_id=$CLIENT_ID" "$_track_json" ||
        { log "resolve failed: $_track_url"; return 1; }
    _track_title=$(json_get "$_track_json" '.title // empty')
    [ -n "$_track_title" ] || {
        log "server response (first 300 bytes): $(head -c 300 "$_track_json" 2>/dev/null)"
        return 1
    }
    log "track: $_track_title"
    _track_stream=$(stream_for_track "$_track_json") || return $?
    log "playing selected stream"
    "$FFMPEG" -nostdin -hide_banner -loglevel warning \
        -i "$_track_stream" -f alsa "$AUDIO_DEV"
}

resolve_user_tracks() {
    _user_permalink=$1
    get_client_id
    _user_json="$TMP/user.json"
    _user_enc=$(urlencode "https://soundcloud.com/$_user_permalink")
    fetch "https://api-v2.soundcloud.com/resolve?url=$_user_enc&client_id=$CLIENT_ID" "$_user_json" ||
        die "user resolve failed: $_user_permalink"
    _user_id=$(json_get "$_user_json" '.id // empty')
    [ -n "$_user_id" ] || die "user not found: $_user_permalink"
    _user_list="$TMP/user-tracks.json"
    fetch "https://api-v2.soundcloud.com/users/$_user_id/tracks?client_id=$CLIENT_ID&limit=50&linked_partitioning=1" "$_user_list" ||
        die "user tracks failed: $_user_permalink"
    "$JQ" -r '.collection[]? | .permalink_url // empty' "$_user_list"
}

resolve_me_tracks() {
    [ -n "$OAUTH_TOKEN" ] || die "SC_OAUTH_TOKEN is required for --me"
    get_client_id
    _me_list="$TMP/me.json"
    if ! "$WGET" -q -O "$_me_list" -U "$UA" --header='Accept-Encoding: identity' \
        --header="Authorization: OAuth $OAUTH_TOKEN" \
        "https://api-v2.soundcloud.com/me/activities?limit=50&linked_partitioning=1"; then
        die "me/activities failed"
    fi
    "$JQ" -r '
      .collection[]? |
      (.origin // .track // .playlist) |
      .permalink_url // empty
    ' "$_me_list"
}

mode=url
arg=
case ${1:-} in
    --select-stream)
        select_stream /dev/stdin
        exit $?
        ;;
    --user)
        mode=user; arg=${2:?--user needs a permalink}
        ;;
    --feed)
        mode=user; arg=${2:?--feed needs a permalink}
        ;;
    --me)
        mode=me
        ;;
    --help|-h)
        sed -n '2,25p' "$0"; exit 0
        ;;
    '')
        die "usage: $0 <track-url> | --user <permalink> | --feed <permalink> | --me"
        ;;
esac

case $mode in
    user)
        resolve_user_tracks "$arg" | while IFS= read -r u; do
            [ -n "$u" ] || continue
            play_track_url "$u" || log "skipped: $u"
        done
        ;;
    me)
        resolve_me_tracks | while IFS= read -r u; do
            [ -n "$u" ] || continue
            play_track_url "$u" || log "skipped: $u"
        done
        ;;
    url)
        play_track_url "$1"
        ;;
esac
