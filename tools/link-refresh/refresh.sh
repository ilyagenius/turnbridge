#!/bin/bash
# refresh.sh — Refreshes Jazz and Telemost room links
# Run via cron: 0 */3 * * * /home/ilya/link-refresh/refresh.sh

set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
LINKS_FILE="$DIR/links.json"
LOG_FILE="$DIR/refresh.log"

log() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" >> "$LOG_FILE"; }

log "=== Starting link refresh ==="

# --- Step 1: Create new Telemost room ---
log "Creating Telemost room..."
TELEMOST_LINK=""
if TELEMOST_LINK=$(node "$DIR/telemost-create.js" 2>>"$LOG_FILE"); then
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
    # Retry with wider window
    sleep 3
    JAZZ_LINK=$(sudo journalctl -u jazz-turn-proxy --since "15 seconds ago" --no-pager 2>/dev/null | grep -oP 'https://salutejazz\.ru/calls/\S+' | tail -1 || true)
fi

if [ -n "$JAZZ_LINK" ]; then
    log "Jazz link: $JAZZ_LINK"
else
    log "WARNING: Could not parse Jazz link from logs"
fi

# Also parse Jazz link from jazz-turn-proxy service
JAZZ_LINK_2=$(sudo journalctl -u jazz-turn-proxy -n 30 --no-pager 2>/dev/null | grep -oP 'Jazz room link: \K\S+' | tail -1 || true)
if [ -n "$JAZZ_LINK_2" ]; then
    JAZZ_LINK="$JAZZ_LINK_2"
    log "Jazz link (from 'room link' log): $JAZZ_LINK"
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
