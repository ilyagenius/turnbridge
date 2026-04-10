#!/bin/bash
# setup.sh — Full TurnBridge server setup from scratch
# Usage: ssh into your VPS and run:
#   curl -sL https://raw.githubusercontent.com/ilyagenius/turnbridge-private/main/server/setup.sh | bash
# Or clone the repo and run: ./server/setup.sh
#
# Requires: Ubuntu 22.04+ / Debian 12+, root or sudo access

set -euo pipefail

# === Configuration (edit these) ===
WG_PORT=51820
WG_ADDR="10.77.77.1/24"
VK_PROXY_PORT=56000
INSTALL_DIR="/opt/turnbridge"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
log()  { echo -e "${GREEN}[+]${NC} $*"; }
warn() { echo -e "${YELLOW}[!]${NC} $*"; }
err()  { echo -e "${RED}[!]${NC} $*"; }

if [ "$(id -u)" -ne 0 ]; then
    err "Run as root or with sudo"
    exit 1
fi

REAL_USER="${SUDO_USER:-$(whoami)}"
log "Setting up TurnBridge server for user: $REAL_USER"

# ============================================================
# 1. System packages
# ============================================================
log "Installing system packages..."
apt-get update -qq
apt-get install -y -qq wireguard wireguard-tools golang-go nodejs npm curl jq > /dev/null

# ============================================================
# 2. WireGuard setup
# ============================================================
if [ ! -f /etc/wireguard/wg0.conf ]; then
    log "Configuring WireGuard..."
    SERVER_PRIVKEY=$(wg genkey)
    SERVER_PUBKEY=$(echo "$SERVER_PRIVKEY" | wg pubkey)

    # Generate a client keypair too
    CLIENT_PRIVKEY=$(wg genkey)
    CLIENT_PUBKEY=$(echo "$CLIENT_PRIVKEY" | wg pubkey)

    cat > /etc/wireguard/wg0.conf <<EOF
[Interface]
PrivateKey = $SERVER_PRIVKEY
Address = $WG_ADDR
ListenPort = $WG_PORT
PostUp = iptables -A FORWARD -i wg0 -j ACCEPT; iptables -t nat -A POSTROUTING -o eth0 -j MASQUERADE
PostDown = iptables -D FORWARD -i wg0 -j ACCEPT; iptables -t nat -D POSTROUTING -o eth0 -j MASQUERADE

[Peer]
# Client
PublicKey = $CLIENT_PUBKEY
AllowedIPs = 10.77.77.2/32
EOF

    chmod 600 /etc/wireguard/wg0.conf

    # Enable IP forwarding
    sysctl -w net.ipv4.ip_forward=1 > /dev/null
    echo "net.ipv4.ip_forward=1" > /etc/sysctl.d/99-turnbridge.conf

    systemctl enable --now wg-quick@wg0

    SERVER_IP=$(curl -s4 ifconfig.me || hostname -I | awk '{print $1}')

    echo ""
    log "WireGuard configured!"
    echo "  Server public key: $SERVER_PUBKEY"
    echo "  Server endpoint:   $SERVER_IP:$WG_PORT"
    echo ""
    warn "Client WireGuard config (save this!):"
    echo "  ----------------------------------------"
    echo "  [Interface]"
    echo "  PrivateKey = $CLIENT_PRIVKEY"
    echo "  Address = 10.77.77.2/24"
    echo "  DNS = 8.8.8.8"
    echo "  MTU = 1180"
    echo ""
    echo "  [Peer]"
    echo "  PublicKey = $SERVER_PUBKEY"
    echo "  AllowedIPs = 0.0.0.0/0, ::/0"
    echo "  Endpoint = $SERVER_IP:$WG_PORT"
    echo "  PersistentKeepalive = 25"
    echo "  ----------------------------------------"
    echo ""
else
    log "WireGuard already configured, skipping"
    systemctl enable --now wg-quick@wg0 2>/dev/null || true
fi

# ============================================================
# 3. Create directory structure
# ============================================================
log "Creating $INSTALL_DIR..."
mkdir -p "$INSTALL_DIR/link-refresh"
chown -R "$REAL_USER:$REAL_USER" "$INSTALL_DIR"

# ============================================================
# 4. vk-turn-proxy (VK / WB TURN relay)
# ============================================================
if [ ! -f "$INSTALL_DIR/vk-turn-proxy" ]; then
    log "Downloading vk-turn-proxy..."
    VK_PROXY_URL="https://github.com/cacggghp/vk-turn-proxy/releases/download/v1.0.0/vk-turn-proxy-linux-amd64"
    curl -sL "$VK_PROXY_URL" -o "$INSTALL_DIR/vk-turn-proxy"
    chmod +x "$INSTALL_DIR/vk-turn-proxy"
    log "Downloaded vk-turn-proxy"
else
    log "vk-turn-proxy already exists, skipping"
fi

cat > /etc/systemd/system/vk-turn-proxy.service <<EOF
[Unit]
Description=VK TURN Proxy
After=network.target wg-quick@wg0.service

[Service]
ExecStart=$INSTALL_DIR/vk-turn-proxy -listen :$VK_PROXY_PORT
Restart=always
RestartSec=3
User=$REAL_USER

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now vk-turn-proxy

# ============================================================
# 5. link-server (Go build)
# ============================================================
log "Building link-server..."

# Check if source exists locally (running from repo)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -f "$SCRIPT_DIR/link-server/main.go" ]; then
    cd "$SCRIPT_DIR/link-server"
    GOFLAGS="" CGO_ENABLED=0 go build -o "$INSTALL_DIR/link-server" .
    log "Built link-server from local source"
else
    # Minimal inline build
    warn "link-server source not found, creating minimal version..."
    cat > /tmp/link-server-main.go <<'GOEOF'
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
)

func main() {
	listen := flag.String("listen", "10.77.77.1:8080", "Listen address")
	linksFile := flag.String("links", "/opt/turnbridge/links.json", "Path to links.json")
	flag.Parse()
	http.HandleFunc("/links", func(w http.ResponseWriter, r *http.Request) {
		data, err := os.ReadFile(*linksFile)
		if err != nil {
			http.Error(w, "links not available", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
		log.Printf("served links to %s", r.RemoteAddr)
	})
	log.Printf("listening on %s", *listen)
	log.Fatal(http.ListenAndServe(*listen, nil))
}
GOEOF
    cd /tmp && go build -o "$INSTALL_DIR/link-server" link-server-main.go
    rm -f link-server-main.go
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

# Create initial empty links.json
if [ ! -f "$INSTALL_DIR/links.json" ]; then
    echo '{"jazz":"","telemost":"","updated":""}' > "$INSTALL_DIR/links.json"
    chown "$REAL_USER:$REAL_USER" "$INSTALL_DIR/links.json"
fi

systemctl daemon-reload
systemctl enable --now link-server

# ============================================================
# 6. Link refresh scripts (Node.js + Playwright)
# ============================================================
log "Setting up link-refresh..."

if [ -d "$SCRIPT_DIR/link-refresh" ]; then
    cp "$SCRIPT_DIR/link-refresh/"*.js "$INSTALL_DIR/link-refresh/"
    cp "$SCRIPT_DIR/link-refresh/refresh.sh" "$INSTALL_DIR/link-refresh/"
    cp "$SCRIPT_DIR/link-refresh/package.json" "$INSTALL_DIR/link-refresh/"
fi
chmod +x "$INSTALL_DIR/link-refresh/refresh.sh"
chown -R "$REAL_USER:$REAL_USER" "$INSTALL_DIR"

log "Installing Playwright..."
cd "$INSTALL_DIR/link-refresh"
sudo -u "$REAL_USER" npm install --omit=dev 2>/dev/null || true
sudo -u "$REAL_USER" npx playwright install chromium --with-deps 2>/dev/null || warn "Playwright install may need manual steps"

# Systemd timers for link refresh
cat > /etc/systemd/system/link-refresh.service <<EOF
[Unit]
Description=TurnBridge Link Refresh

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

systemctl daemon-reload
systemctl enable link-refresh.timer yandex-session.timer
systemctl start link-refresh.timer yandex-session.timer

# ============================================================
# 7. Sudoers for link-refresh (needs to restart jazz-proxy)
# ============================================================
log "Configuring sudoers for link-refresh..."
cat > /etc/sudoers.d/turnbridge <<EOF
$REAL_USER ALL=(ALL) NOPASSWD: /usr/bin/systemctl daemon-reload
$REAL_USER ALL=(ALL) NOPASSWD: /usr/bin/systemctl restart jazz-proxy
$REAL_USER ALL=(ALL) NOPASSWD: /usr/bin/journalctl *
$REAL_USER ALL=(ALL) NOPASSWD: /usr/bin/sed -i *jazz-proxy*
$REAL_USER ALL=(ALL) NOPASSWD: /bin/mv /tmp/*.service /tmp/*.timer /etc/systemd/system/
EOF
chmod 440 /etc/sudoers.d/turnbridge

# ============================================================
# Done
# ============================================================
echo ""
echo "============================================"
log "TurnBridge server setup complete!"
echo "============================================"
echo ""
echo "Running services:"
echo "  - wireguard (wg0)         : $(systemctl is-active wg-quick@wg0)"
echo "  - vk-turn-proxy (:$VK_PROXY_PORT) : $(systemctl is-active vk-turn-proxy)"
echo "  - link-server (10.77.77.1:8080): $(systemctl is-active link-server)"
echo "  - link-refresh.timer      : $(systemctl is-active link-refresh.timer)"
echo "  - yandex-session.timer    : $(systemctl is-active yandex-session.timer)"
echo ""
warn "Manual steps remaining:"
echo "  1. Place jazz-turn-proxy and jazz-proxy binaries in $INSTALL_DIR/"
echo "     (distributed privately — contact @ilkl34 on Telegram)"
echo "  2. Create their systemd services"
echo "  3. Run initial Yandex login:"
echo "       cd $INSTALL_DIR/link-refresh && node yandex-login.js"
echo "  4. Run first link refresh:"
echo "       $INSTALL_DIR/link-refresh/refresh.sh"
echo "  5. Generate client config link:"
echo "       python3 quick_link.py"
echo ""
