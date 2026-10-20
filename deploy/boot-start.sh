#!/bin/sh
set -u

LOG=/data/prime4-app/boot-start.log
LOCK=/run/prime4-app-boot-start.$(cat /proc/sys/kernel/random/boot_id)
ENV_FILE=/data/prime4-app/soundcloud-eaas.env

log() {
    echo "$(date -u +%FT%TZ) $*" >>"$LOG" 2>/dev/null
}

mkdir "$LOCK" 2>/dev/null || exit 0
LOCAL_IF=$(sed -n 's/^SC_LOCAL_IF=//p' "$ENV_FILE" 2>/dev/null | tail -1)
LOCAL_IF=${LOCAL_IF:-wlan0}
log "waiting for network on $LOCAL_IF"

for _ in $(seq 1 120); do
    if [ -x /data/prime4-app/bin/soundcloud-eaas ] &&
        [ -f "$ENV_FILE" ] &&
        /sbin/ip route 2>/dev/null | grep -q '^default ' &&
        /sbin/ip -4 addr show dev "$LOCAL_IF" 2>/dev/null | grep -q 'inet ' &&
        wget -q -T 3 -O /dev/null http://ipv4.connman.inmusicbrands.com/; then
        systemctl daemon-reload >/dev/null 2>&1 || true
        systemctl reset-failed soundcloud-eaas >/dev/null 2>&1 || true
        if systemctl start soundcloud-eaas >>"$LOG" 2>&1; then
            log "soundcloud-eaas started"
            exit 0
        fi
    fi
    sleep 5
done

log "soundcloud-eaas did not start"
exit 1
