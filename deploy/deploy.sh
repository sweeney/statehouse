#!/usr/bin/env bash
#
# Build and deploy statehouse to a remote host.
#
# Usage:
#   ./deploy/deploy.sh sweeney@garibaldi
#
# Keeps the last 3 versioned binaries in /opt/statehouse/bin/ and symlinks
# the active one. Restarts the statehouse service after upload.
# Requires passwordless sudo for systemctl on the remote.
#
# First-time setup: run deploy/setup.sh on the target host with sudo.
#
set -euo pipefail

REMOTE="${1:?Usage: $0 user@host}"
SERVICE="statehouse"
BINARY="statehouse"
BUILD_DIR="bin"
DEPLOY_DIR="/opt/statehouse/bin"
KEEP_VERSIONS=3

VERSION=$(date +%Y%m%d-%H%M%S)
COMMIT=$(git rev-parse --short HEAD)
REMOTE_BIN="${BINARY}-${VERSION}"

echo "=== Building $BINARY (linux/amd64) ==="
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-X main.version=${COMMIT}" -o "$BUILD_DIR/$BINARY" ./cmd/statehouse/
echo "  Built: $BUILD_DIR/$BINARY"

echo "=== Uploading to $REMOTE ==="
scp "$BUILD_DIR/$BINARY" "$REMOTE:$DEPLOY_DIR/$REMOTE_BIN"
ssh "$REMOTE" "chmod 755 $DEPLOY_DIR/$REMOTE_BIN"

echo "=== Activating $REMOTE_BIN ==="
ssh "$REMOTE" "ln -sfn $REMOTE_BIN $DEPLOY_DIR/$BINARY"

echo "=== Restarting $SERVICE ==="
ssh "$REMOTE" "sudo systemctl restart $SERVICE"

echo "=== Verifying ==="
sleep 2

if ssh "$REMOTE" "sudo systemctl is-active --quiet $SERVICE"; then
    echo "  ✓ $SERVICE is running"
else
    echo "  ✗ $SERVICE failed to start"
    ssh "$REMOTE" "sudo journalctl -u $SERVICE -n 20 --no-pager"
    exit 1
fi

if ssh "$REMOTE" "sudo journalctl -u $SERVICE -n 20 --no-pager" \
        | grep -qE "invalid_client|identity token fetch failed"; then
    echo ""
    echo "  ✗ CREDENTIAL ERROR: identity auth failed on $REMOTE"
    echo "    Update identity.client_secret in /etc/statehouse/config.yaml"
    echo "    then: sudo systemctl restart $SERVICE"
    echo ""
    exit 1
fi
echo "  ✓ no credential errors"

echo "=== Cleaning old versions (keeping $KEEP_VERSIONS) ==="
ssh "$REMOTE" "\
  cd $DEPLOY_DIR && \
  ls -t ${BINARY}-* \
    | tail -n +$((KEEP_VERSIONS + 1)) \
    | xargs -r rm --"

echo ""
echo "=== Deployed $VERSION ($COMMIT) ==="
ssh "$REMOTE" "sudo journalctl -u $SERVICE -n 5 --no-pager"
