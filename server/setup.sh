#!/bin/bash
# setup.sh — Full TurnBridge server setup
# Clones, builds, and configures everything automatically.
# User only needs to: run yandex-login.js for cookies (one-time).
#
# Usage:
#   git clone <turnbridge-private repo>
#   cd turnbridge-private/server
#   sudo GH_TOKEN_VK=<token> GH_TOKEN_JAZZ=<token> ./setup.sh
#
# Tokens are needed to clone private repos (vk-turn-proxy-v2, jazz-turn-proxy).
# If bins/ directory has pre-built binaries, tokens are not required.

set -euo pipefail

# === Configuration ===
WG_PORT=51820
WG_ADDR="10.77.77.1/24"
VK_PROXY_PORT=56000
INSTALL_DIR="/opt/turnbridge"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
log()  { echo -e "${GREEN}[+]${NC} $*"; }
warn() { echo -e "${YELLOW}[!]${NC} $*"; }
err()  { echo -e "${RED}[x]${NC} $*"; }

if [ "$(id -u)" -ne 0 ]; then
    err "Run as root: sudo GH_TOKEN_VK=... GH_TOKEN_JAZZ=... ./setup.sh"
    exit 1
fi

REAL_USER="${SUDO_USER:-$(whoami)}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

GH_TOKEN_VK="${GH_TOKEN_VK:-}"
GH_TOKEN_JAZZ="${GH_TOKEN_JAZZ:-}"

log "TurnBridge server setup for user: $REAL_USER"
echo ""

# ============================================================
# 1. System packages
# ============================================================
log "Installing system packages..."
apt-get update -qq
apt-get install -y -qq wireguard wireguard-tools golang-go nodejs npm curl jq > /dev/null
log "Packages installed"

# ============================================================
# 2. WireGuard
# ============================================================
if [ ! -f /etc/wireguard/wg0.conf ]; then
    log "Configuring WireGuard..."
    SERVER_PRIVKEY=$(wg genkey)
    SERVER_PUBKEY=$(echo "$SERVER_PRIVKEY" | wg pubkey)
    CLIENT_PRIVKEY=$(wg genkey)
    CLIENT_PUBKEY=$(echo "$CLIENT_PRIVKEY" | wg pubkey)

    # Detect main network interface
    MAIN_IFACE=$(ip route show default | awk '/default/ {print $5}' | head -1)
    [ -z "$MAIN_IFACE" ] && MAIN_IFACE="eth0"

    cat > /etc/wireguard/wg0.conf <<EOF
[Interface]
PrivateKey = $SERVER_PRIVKEY
Address = $WG_ADDR
ListenPort = $WG_PORT
PostUp = iptables -A FORWARD -i wg0 -j ACCEPT; iptables -t nat -A POSTROUTING -o $MAIN_IFACE -j MASQUERADE
PostDown = iptables -D FORWARD -i wg0 -j ACCEPT; iptables -t nat -D POSTROUTING -o $MAIN_IFACE -j MASQUERADE

[Peer]
PublicKey = $CLIENT_PUBKEY
AllowedIPs = 10.77.77.2/32
EOF
    chmod 600 /etc/wireguard/wg0.conf

    sysctl -w net.ipv4.ip_forward=1 > /dev/null
    echo "net.ipv4.ip_forward=1" > /etc/sysctl.d/99-turnbridge.conf

    systemctl enable --now wg-quick@wg0

    SERVER_IP=$(curl -s4 --connect-timeout 5 ifconfig.me 2>/dev/null || hostname -I | awk '{print $1}')

    # Save client config
    mkdir -p "$INSTALL_DIR"
    cat > "$INSTALL_DIR/client_wg.conf" <<EOF
[Interface]
PrivateKey = $CLIENT_PRIVKEY
Address = 10.77.77.2/24
DNS = 8.8.8.8
MTU = 1180

[Peer]
PublicKey = $SERVER_PUBKEY
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = $SERVER_IP:$WG_PORT
PersistentKeepalive = 25
EOF
    log "WireGuard configured (client config: $INSTALL_DIR/client_wg.conf)"
else
    log "WireGuard already configured"
    systemctl enable --now wg-quick@wg0 2>/dev/null || true
fi

# ============================================================
# 3. Directory structure
# ============================================================
mkdir -p "$INSTALL_DIR/link-refresh"
chown -R "$REAL_USER:$REAL_USER" "$INSTALL_DIR"

# ============================================================
# Helper: install Go binary (bins/ -> clone+build -> fail)
# ============================================================
install_go_binary() {
    local NAME="$1"
    local REPO_URL="$2"
    local BUILD_SUBDIR="$3"
    local OUTPUT="$INSTALL_DIR/$NAME"

    # Already installed?
    if [ -f "$OUTPUT" ] && [ "$(stat -c%s "$OUTPUT" 2>/dev/null || echo 0)" -gt 1000 ]; then
        log "$NAME already installed ($(du -h "$OUTPUT" | cut -f1))"
        return 0
    fi

    # Try 1: pre-built in bins/
    for BINS_DIR in "$SCRIPT_DIR/bins" "$REPO_ROOT/bins"; do
        if [ -f "$BINS_DIR/$NAME" ] && [ "$(stat -c%s "$BINS_DIR/$NAME")" -gt 1000 ]; then
            cp "$BINS_DIR/$NAME" "$OUTPUT"
            chmod +x "$OUTPUT"
            log "$NAME installed from bins/ ($(du -h "$OUTPUT" | cut -f1))"
            return 0
        fi
    done

    # Try 2: clone and build
    if [ -z "$REPO_URL" ]; then
        err "$NAME: not found in bins/ and no repo URL provided"
        err "Either place pre-built binary in server/bins/$NAME or provide GH_TOKEN"
        return 1
    fi

    if ! command -v go >/dev/null 2>&1; then
        err "$NAME: not found in bins/ and Go is not installed"
        return 1
    fi

    log "Building $NAME from source..."
    local TMP_DIR
    TMP_DIR=$(mktemp -d)
    if ! git clone "$REPO_URL" "$TMP_DIR" >/dev/null 2>&1; then
        rm -rf "$TMP_DIR"
        err "$NAME: git clone failed — check your token"
        return 1
    fi

    cd "$TMP_DIR/$BUILD_SUBDIR"
    if ! CGO_ENABLED=0 go build -o "$OUTPUT" . 2>&1; then
        rm -rf "$TMP_DIR"
        err "$NAME: go build failed"
        return 1
    fi
    rm -rf "$TMP_DIR"
    chmod +x "$OUTPUT"
    log "$NAME built from source ($(du -h "$OUTPUT" | cut -f1))"
}

# ============================================================
# 4. vk-turn-proxy (VK TURN relay v2, multi-stream)
# ============================================================
VK_REPO=""
[ -n "$GH_TOKEN_VK" ] && VK_REPO="https://${GH_TOKEN_VK}@github.com/ilyagenius/vk-turn-proxy-v2.git"
install_go_binary "vk-turn-proxy" "$VK_REPO" "server" || exit 1

cat > /etc/systemd/system/vk-turn-proxy.service <<EOF
[Unit]
Description=VK TURN Proxy
After=network.target wg-quick@wg0.service

[Service]
ExecStart=$INSTALL_DIR/vk-turn-proxy -listen :$VK_PROXY_PORT -connect 127.0.0.1:$WG_PORT
Restart=always
RestartSec=3
User=$REAL_USER

[Install]
WantedBy=multi-user.target
EOF

# ============================================================
# 5. jazz-turn-proxy (Jazz + Telemost WebRTC bridge)
# ============================================================
JAZZ_REPO=""
[ -n "$GH_TOKEN_JAZZ" ] && JAZZ_REPO="https://${GH_TOKEN_JAZZ}@github.com/ilyagenius/jazz-turn-proxy.git"
install_go_binary "jazz-turn-proxy" "$JAZZ_REPO" "." || exit 1

cat > /etc/systemd/system/jazz-proxy.service <<EOF
[Unit]
Description=Jazz Proxy (creates Jazz room, bridges WebRTC to WireGuard)
After=network.target wg-quick@wg0.service

[Service]
ExecStart=$INSTALL_DIR/jazz-turn-proxy -wg 127.0.0.1:$WG_PORT
Restart=always
RestartSec=5
User=$REAL_USER

[Install]
WantedBy=multi-party.target
EOF

cat > /etc/systemd/system/telemost-bridge.service <<EOF
[Unit]
Description=Telemost Bridge (bridges Telemost WebRTC to WireGuard)
After=network.target wg-quick@wg0.service

[Service]
ExecStart=$INSTALL_DIR/jazz-turn-proxy --telemost-room https://telemost.yandex.ru/j/PLACEHOLDER -wg 127.0.0.1:$WG_PORT
Restart=always
RestartSec=5
User=$REAL_USER

[Install]
WantedBy=multi-user.target
EOF

# ============================================================
# 6. link-server
# ============================================================
if [ -f "$SCRIPT_DIR/link-server/main.go" ] && command -v go >/dev/null 2>&1; then
    log "Building link-server from source..."
    cd "$SCRIPT_DIR/link-server"
    GOFLAGS="" CGO_ENABLED=0 go build -o "$INSTALL_DIR/link-server" .
    log "link-server built"
else
    install_go_binary "link-server" "" "" || exit 1
fi

if [ ! -f "$INSTALL_DIR/links.json" ]; then
    echo '{"jazz":"","telemost":"","updated":""}' > "$INSTALL_DIR/links.json"
    chown "$REAL_USER:$REAL_USER" "$INSTALL_DIR/links.json"
fi

cat > /etc/systemd/system/link-server.service <<EOF
[Unit]
Description=TurnBridge Link Server
After=network.target wg-quick@wg0.service

[Service]
ExecStart=$INSTALL_DIR/link-server -listen 10.77.77.1:8080 -links $INSTALL_DIR/links.json
Restart=always
RestartSec=3
User=$REAL_USER

[Install]
WantedBy=multi-user.target
EOF

# ============================================================
# 7. Link refresh (Node.js + Playwright)
# ============================================================
log "Setting up link-refresh..."
cp "$SCRIPT_DIR/link-refresh/"*.js "$INSTALL_DIR/link-refresh/" 2>/dev/null || true
cp "$SCRIPT_DIR/link-refresh/refresh.sh" "$INSTALL_DIR/link-refresh/" 2>/dev/null || true
cp "$SCRIPT_DIR/link-refresh/package.json" "$INSTALL_DIR/link-refresh/" 2>/dev/null || true
chmod +x "$INSTALL_DIR/link-refresh/refresh.sh" 2>/dev/null || true
chown -R "$REAL_USER:$REAL_USER" "$INSTALL_DIR"

log "Installing Node.js deps + Playwright..."
cd "$INSTALL_DIR/link-refresh"
sudo -u "$REAL_USER" npm install --omit=dev 2>/dev/null || warn "npm install failed"
sudo -u "$REAL_USER" npx playwright install chromium --with-deps 2>/dev/null || warn "Playwright install needs manual fix"

cat > /etc/systemd/system/link-refresh.service <<EOF
[Unit]
Description=TurnBridge Link Refresh (Jazz + Telemost)

[Service]
Type=oneshot
ExecStart=$INSTALL_DIR/link-refresh/refresh.sh
User=$REAL_USER
WorkingDirectory=$INSTALL_DIR
EOF

cat > /etc/systemd/system/link-refresh.timer <<EOF
[Unit]
Description=Refresh Jazz/Telemost links every 3 hours

[Timer]
OnCalendar=*-*-* 00/3:00:00
Persistent=true

[Install]
WantedBy=timers.target
EOF

cat > /etc/systemd/system/yandex-session.service <<EOF
[Unit]
Description=Refresh Yandex session cookies

[Service]
Type=oneshot
ExecStart=/usr/bin/node $INSTALL_DIR/link-refresh/yandex-refresh-session.js
User=$REAL_USER
WorkingDirectory=$INSTALL_DIR
EOF

cat > /etc/systemd/system/yandex-session.timer <<EOF
[Unit]
Description=Refresh Yandex session every 12 hours

[Timer]
OnCalendar=*-*-* 00/12:00:00
Persistent=true

[Install]
WantedBy=timers.target
EOF

# ============================================================
# 8. Sudoers for link-refresh
# ============================================================
cat > /etc/sudoers.d/turnbridge <<SUDOEOF
$REAL_USER ALL=(ALL) NOPASSWD: /usr/bin/systemctl daemon-reload
$REAL_USER ALL=(ALL) NOPASSWD: /usr/bin/systemctl restart jazz-proxy
$REAL_USER ALL=(ALL) NOPASSWD: /usr/bin/systemctl restart telemost-bridge
$REAL_USER ALL=(ALL) NOPASSWD: /usr/bin/journalctl *
$REAL_USER ALL=(ALL) NOPASSWD: /usr/bin/sed -i *
$REAL_USER ALL=(ALL) NOPASSWD: /bin/mv /tmp/*.service /tmp/*.timer /etc/systemd/system/
SUDOEOF
chmod 440 /etc/sudoers.d/turnbridge

# ============================================================
# 9. Start everything
# ============================================================
log "Starting all services..."
systemctl daemon-reload
fuser -k $VK_PROXY_PORT/udp 2>/dev/null || true
systemctl enable --now vk-turn-proxy jazz-proxy link-server 2>/dev/null
systemctl enable --now link-refresh.timer yandex-session.timer 2>/dev/null
systemctl enable telemost-bridge 2>/dev/null

# ============================================================
# Done
# ============================================================
echo ""
echo -e "${GREEN}============================================${NC}"
log "TurnBridge server setup complete!"
echo -e "${GREEN}============================================${NC}"
echo ""
echo "Services:"
echo "  - wireguard (wg0)          : $(systemctl is-active wg-quick@wg0 2>/dev/null || echo 'inactive')"
echo "  - vk-turn-proxy (:$VK_PROXY_PORT)  : $(systemctl is-active vk-turn-proxy 2>/dev/null || echo 'inactive')"
echo "  - jazz-proxy               : $(systemctl is-active jazz-proxy 2>/dev/null || echo 'inactive')"
echo "  - telemost-bridge          : $(systemctl is-active telemost-bridge 2>/dev/null || echo 'inactive')"
echo "  - link-server (10.77.77.1) : $(systemctl is-active link-server 2>/dev/null || echo 'inactive')"
echo "  - link-refresh.timer       : $(systemctl is-active link-refresh.timer 2>/dev/null || echo 'inactive')"
echo "  - yandex-session.timer     : $(systemctl is-active yandex-session.timer 2>/dev/null || echo 'inactive')"
echo ""

if [ -f "$INSTALL_DIR/client_wg.conf" ]; then
    echo -e "${YELLOW}Client WG config: $INSTALL_DIR/client_wg.conf${NC}"
    echo ""
fi

echo -e "${YELLOW}Last step — Yandex cookies (one-time):${NC}"
echo "  cd $INSTALL_DIR/link-refresh && node yandex-login.js"
echo "  $INSTALL_DIR/link-refresh/refresh.sh"
echo ""
echo "Then generate client link:"
echo "  cd $REPO_ROOT && python3 quick_link.py"
echo ""
