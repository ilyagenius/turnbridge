#!/bin/bash
# deploy.sh — One-command deploy of TurnBridge server components
# Usage: ./deploy.sh [server_ip]
#
# Deploys: link-server, link-refresh scripts, systemd units
# Requires: Go (for building), SSH access to server

set -euo pipefail

SERVER="${1:-158.160.248.141}"
SSH_KEY="${SSH_KEY:-$HOME/.ssh/yandex_ru_vps}"
USER="ilya"
DEST="/opt/turnbridge"
DIR="$(cd "$(dirname "$0")" && pwd)"

ssh_cmd() { ssh -i "$SSH_KEY" "$USER@$SERVER" "$@"; }
scp_cmd() { scp -i "$SSH_KEY" "$@"; }

echo "=== Building link-server ==="
cd "$DIR/link-server"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$DIR/build/link-server" .
echo "Built: $DIR/build/link-server"

echo "=== Deploying to $SERVER ==="
ssh_cmd "sudo mkdir -p $DEST/link-refresh && sudo chown -R $USER:$USER $DEST"

# Upload binaries and scripts
scp_cmd "$DIR/build/link-server" "$USER@$SERVER:$DEST/link-server"
scp_cmd "$DIR/link-refresh/refresh.sh" \
        "$DIR/link-refresh/telemost-create.js" \
        "$DIR/link-refresh/yandex-refresh-session.js" \
        "$DIR/link-refresh/yandex-login.js" \
        "$DIR/link-refresh/package.json" \
        "$USER@$SERVER:$DEST/link-refresh/"

ssh_cmd "chmod +x $DEST/link-refresh/refresh.sh"

# Upload and install systemd units
scp_cmd "$DIR/systemd/"*.service "$DIR/systemd/"*.timer "$USER@$SERVER:/tmp/"
ssh_cmd "sudo mv /tmp/link-server.service /tmp/link-refresh.service /tmp/link-refresh.timer \
         /tmp/yandex-session.service /tmp/yandex-session.timer /etc/systemd/system/ && \
         sudo systemctl daemon-reload"

echo "=== Restarting services ==="
ssh_cmd "sudo systemctl restart link-server"
ssh_cmd "sudo systemctl enable link-refresh.timer yandex-session.timer"
ssh_cmd "sudo systemctl start link-refresh.timer yandex-session.timer"

echo "=== Status ==="
ssh_cmd "systemctl status link-server link-refresh.timer yandex-session.timer --no-pager -l" || true

echo ""
echo "Done. First time? Run on server:"
echo "  cd $DEST/link-refresh && npm install && npx playwright install chromium --with-deps"
echo "  node yandex-login.js  # one-time interactive login"
