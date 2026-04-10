#!/bin/bash
# refresh.sh — Refreshes Jazz and Telemost room links
# Runs as systemd timer (link-refresh.timer) every 3 hours

set -euo pipefail

DIR=/opt/turnbridge
LINKS_FILE="$DIR/links.json"
SCRIPTS_DIR="$DIR/link-refresh"
LOG_TAG="link-refresh"

log() { logger -t "$LOG_TAG" "$*"; echo "[$(date '+%H:%M:%S')] $*"; }

log "=== Starting link refresh ==="

# --- Step 1: Create new Telemost room ---
log "Creating Telemost room..."
TELEMOST_LINK=""
if TELEMOST_LINK=$(node "$SCRIPTS_DIR/telemost-create.js" 2>&1); then
    log "Telemost link: $TELEMOST_LINK"
else
    log "ERROR: Failed to create Telemost room"
    TELEMOST_LINK=""
fi

# --- Step 2: Update jazz-proxy service with new Telemost link ---
if [ -n "$TELEMOST_LINK" ]; then
    sudo sed -i "s|--telemost-room https://telemost.yandex.ru/j/[^ ]*|--telemost-room $TELEMOST_LINK|" /etc/systemd/system/jazz-proxy.service
    log "Updated jazz-proxy.service with new Telemost link"
fi

# --- Step 3: Restart jazz-proxy (this also regenerates Jazz room) ---
sudo systemctl daemon-reload
sudo systemctl restart jazz-proxy
log "Restarted jazz-proxy"

# --- Step 4: Wait and parse Jazz link from logs ---
sleep 5
JAZZ_LINK=$(sudo journalctl -u jazz-turn-proxy --since "5 seconds ago" --no-pager 2>/dev/null | grep -oP 'https://salutejazz\.ru/calls/\S+' | tail -1 || true)

if [ -z "$JAZZ_LINK" ]; then
    sleep 3
    JAZZ_LINK=$(sudo journalctl -u jazz-turn-proxy --since "15 seconds ago" --no-pager 2>/dev/null | grep -oP 'https://salutejazz\.ru/calls/\S+' | tail -1 || true)
fi

if [ -n "$JAZZ_LINK" ]; then
    log "Jazz link: $JAZZ_LINK"
else
    log "WARNING: Could not parse Jazz link from logs"
fi

# Also try parsing from "Jazz room link:" format
JAZZ_LINK_2=$(sudo journalctl -u jazz-turn-proxy -n 30 --no-pager 2>/dev/null | grep -oP 'Jazz room link: \K\S+' | tail -1 || true)
if [ -n "$JAZZ_LINK_2" ]; then
    JAZZ_LINK="$JAZZ_LINK_2"
    log "Jazz link (from room log): $JAZZ_LINK"
fi

# --- Step 5: Write links.json ---
cat > "$LINKS_FILE" <<EOF
{
  "jazz": "$JAZZ_LINK",
  "telemost": "$TELEMOST_LINK",
  "updated": "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
}
EOF

log "Written $LINKS_FILE"
log "=== Refresh complete ==="
