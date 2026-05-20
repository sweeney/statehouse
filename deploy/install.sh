#!/usr/bin/env bash
#
# One-time install for statehouse on the target host.
# Run as root or with sudo — no repo checkout required.
#
# Usage (from dev machine):
#   scp deploy/install.sh sweeney@192.168.1.200:/tmp/
#   ssh -t sweeney@192.168.1.200 sudo sh /tmp/install.sh
#
set -euo pipefail

SERVICE=statehouse

echo "=== Creating user ==="
if ! id "$SERVICE" &>/dev/null; then
    useradd --system --shell /usr/sbin/nologin --home-dir "/var/lib/$SERVICE" "$SERVICE"
    echo "  Created: $SERVICE"
else
    echo "  User already exists"
fi

echo "=== Creating directories ==="
mkdir -p /opt/$SERVICE/bin
chown "${SUDO_USER:-root}:${SUDO_USER:-root}" /opt/$SERVICE/bin
echo "  /opt/$SERVICE/bin"

mkdir -p /var/lib/$SERVICE
chown $SERVICE:$SERVICE /var/lib/$SERVICE
chmod 700 /var/lib/$SERVICE
echo "  /var/lib/$SERVICE"

mkdir -p /etc/$SERVICE
chown root:$SERVICE /etc/$SERVICE
chmod 750 /etc/$SERVICE
echo "  /etc/$SERVICE"

echo "=== Installing config ==="
if [ ! -f /etc/$SERVICE/config.yaml ]; then
    cat > /etc/$SERVICE/config.yaml << 'CONFIG'
mqtt:
  broker: tcp://192.168.1.200:1883
  client_id: statehouse
  publish_prefix: house

identity:
  base_url: https://id.swee.net
  client_id: statehouse
  client_secret: CcLytMcR6fr9gdtoY6fwtowzzTpjaF94Z7ZvAtzakfc

remote_config:
  base_url: https://config.swee.net

http:
  listen: :8383

recent_log:
  path: /var/lib/statehouse/events.jsonl
  retention_hours: 72
  max_size_mb: 256

influx:
  enabled: false
CONFIG
    chown root:$SERVICE /etc/$SERVICE/config.yaml
    chmod 640 /etc/$SERVICE/config.yaml
    echo "  Installed /etc/$SERVICE/config.yaml"
else
    echo "  /etc/$SERVICE/config.yaml already exists, skipping"
fi

echo "=== Installing systemd unit ==="
cat > /etc/systemd/system/$SERVICE.service << 'UNIT'
[Unit]
Description=House state engine
After=network-online.target
Wants=network-online.target
Documentation=https://github.com/sweeney/statehouse

[Service]
Type=simple
User=statehouse
Group=statehouse

ExecStart=/opt/statehouse/bin/statehouse -config /etc/statehouse/config.yaml
WorkingDirectory=/var/lib/statehouse

Restart=always
RestartSec=5

# Hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/statehouse
ReadOnlyPaths=/etc/statehouse
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
MemoryDenyWriteExecute=true
LockPersonality=true

StandardOutput=journal
StandardError=journal
SyslogIdentifier=statehouse

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable $SERVICE
echo "  Installed and enabled statehouse.service"

echo ""
echo "=== Sudoers ==="
echo "  Add this line to /etc/sudoers.d/statehouse-deploy:"
echo "  sweeney ALL=(ALL) NOPASSWD: /usr/bin/systemctl, /usr/bin/journalctl"
echo ""
echo "=== Done ==="
echo "  Deploy binary: make deploy (from dev machine)"
echo "  Check status:  sudo systemctl status statehouse"
echo "  View logs:     sudo journalctl -u statehouse -f"
