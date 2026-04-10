SERVER  ?= 158.160.248.141
SSH_KEY ?= ~/.ssh/yandex_ru_vps
USER    ?= ilya
DEST    ?= /opt/turnbridge

SSH     = ssh -i $(SSH_KEY) $(USER)@$(SERVER)
SCP     = scp -i $(SSH_KEY)

.PHONY: build deploy install status logs

# Build link-server binary for Linux amd64
build:
	cd link-server && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o ../build/link-server .

# Deploy everything to server
deploy: build
	$(SSH) "sudo mkdir -p $(DEST)/link-refresh && sudo chown -R $(USER):$(USER) $(DEST)"
	$(SCP) build/link-server $(USER)@$(SERVER):$(DEST)/link-server
	$(SCP) link-refresh/refresh.sh link-refresh/telemost-create.js \
	       link-refresh/yandex-refresh-session.js link-refresh/yandex-login.js \
	       link-refresh/package.json \
	       $(USER)@$(SERVER):$(DEST)/link-refresh/
	$(SCP) systemd/*.service systemd/*.timer $(USER)@$(SERVER):/tmp/
	$(SSH) "chmod +x $(DEST)/link-refresh/refresh.sh"
	$(SSH) "sudo mv /tmp/link-server.service /tmp/link-refresh.service /tmp/link-refresh.timer \
	        /tmp/yandex-session.service /tmp/yandex-session.timer /etc/systemd/system/"
	$(SSH) "sudo systemctl daemon-reload"
	@echo "Deployed. Run 'make install' on first setup."

# First-time install: enable services + install node deps
install: deploy
	$(SSH) "cd $(DEST)/link-refresh && npm install --omit=dev"
	$(SSH) "npx playwright install chromium --with-deps || true"
	$(SSH) "sudo systemctl enable --now link-server link-refresh.timer yandex-session.timer"
	@echo "Installed and started."

# Check status of all services
status:
	$(SSH) "systemctl status link-server link-refresh.timer yandex-session.timer --no-pager -l"

# Tail logs
logs:
	$(SSH) "sudo journalctl -u link-server -u link-refresh -u yandex-session -f --no-pager"
