#!/usr/bin/env bash
#
# Updates an existing unbound-dash installation to a new release.
#
# Usage:
#   sudo ./update.sh [-v VERSION] [-f LOCAL_BINARY]
#
#   -v VERSION       Release tag to download (default: latest). Requires
#                     GITHUB_TOKEN env var if the repo is private.
#   -f LOCAL_BINARY  Use a local binary instead of downloading a release.
#
set -euo pipefail

REPO="airkingbr/unbound-dash"
VERSION="latest"
LOCAL_BINARY=""

while getopts "v:f:h" opt; do
  case "$opt" in
    v) VERSION="$OPTARG" ;;
    f) LOCAL_BINARY="$OPTARG" ;;
    h)
      grep '^#' "$0" | sed 's/^# \?//'
      exit 0
      ;;
    *) exit 1 ;;
  esac
done

if [ "$(id -u)" -ne 0 ]; then
  echo "this script must be run as root" >&2
  exit 1
fi

ARCH="$(dpkg --print-architecture)"
case "$ARCH" in
  amd64) GOARCH="amd64" ;;
  arm64) GOARCH="arm64" ;;
  *)
    echo "unsupported architecture: $ARCH" >&2
    exit 1
    ;;
esac

BIN_PATH="/usr/local/bin/unbound-dash"

if [ ! -f "$BIN_PATH" ]; then
  echo "unbound-dash not found at ${BIN_PATH}; run install.sh first" >&2
  exit 1
fi

TMP_FILE="$(mktemp)"
trap 'rm -f "$TMP_FILE"' EXIT

echo "==> Fetching unbound-dash binary"
if [ -n "$LOCAL_BINARY" ]; then
  cp "$LOCAL_BINARY" "$TMP_FILE"
else
  if ! command -v jq >/dev/null 2>&1; then
    apt-get update -qq
    apt-get install -y jq
  fi

  CURL_AUTH=()
  if [ -n "${GITHUB_TOKEN:-}" ]; then
    CURL_AUTH=(-H "Authorization: token ${GITHUB_TOKEN}")
  fi

  if [ "$VERSION" = "latest" ]; then
    API_URL="https://api.github.com/repos/${REPO}/releases/latest"
  else
    API_URL="https://api.github.com/repos/${REPO}/releases/tags/${VERSION}"
  fi

  ASSET_NAME="unbound-dash_linux_${GOARCH}"
  RELEASE_JSON="$(curl -fsSL "${CURL_AUTH[@]}" "$API_URL")"
  RELEASE_TAG="$(echo "$RELEASE_JSON" | jq -r '.tag_name')"
  ASSET_ID="$(echo "$RELEASE_JSON" | jq -r --arg name "$ASSET_NAME" '.assets[] | select(.name == $name) | .id' | head -n1)"

  if [ -z "$ASSET_ID" ] || [ "$ASSET_ID" = "null" ]; then
    echo "could not find a release asset matching ${ASSET_NAME} (version: ${VERSION})" >&2
    echo "pass -f LOCAL_BINARY to update from a local build instead" >&2
    exit 1
  fi

  curl -fsSL "${CURL_AUTH[@]}" -H "Accept: application/octet-stream" \
    -o "$TMP_FILE" "https://api.github.com/repos/${REPO}/releases/assets/${ASSET_ID}"
  echo "    new version: ${RELEASE_TAG}"
fi

chmod +x "$TMP_FILE"

echo "==> Checking forward zones support (forwardzone.conf)"
CONFIG_PATH="/etc/unbound-dash/config.json"
UNBOUND_CONF="/etc/unbound/unbound.conf"
if [ -f "$CONFIG_PATH" ]; then
  CONF_FROM_CONFIG="$(grep -o '"unbound_conf": *"[^"]*"' "$CONFIG_PATH" | sed -E 's/.*"([^"]*)"$/\1/' || true)"
  [ -n "$CONF_FROM_CONFIG" ] && UNBOUND_CONF="$CONF_FROM_CONFIG"
fi
FORWARDZONE_FILE="$(dirname "$UNBOUND_CONF")/unbound.conf.d/forwardzone.conf"
if [ -f "$CONFIG_PATH" ]; then
  FZ_FROM_CONFIG="$(grep -o '"forward_zone_file": *"[^"]*"' "$CONFIG_PATH" | sed -E 's/.*"([^"]*)"$/\1/' || true)"
  [ -n "$FZ_FROM_CONFIG" ] && FORWARDZONE_FILE="$FZ_FROM_CONFIG"
fi
if [ -f "$UNBOUND_CONF" ]; then
  mkdir -p "$(dirname "$FORWARDZONE_FILE")"
  if [ ! -f "$FORWARDZONE_FILE" ]; then
    : > "$FORWARDZONE_FILE"
  fi
  if ! grep -q "forwardzone.conf" "$UNBOUND_CONF" 2>/dev/null; then
    cp "$UNBOUND_CONF" "${UNBOUND_CONF}.bak.$(date +%s)"
    printf '\ninclude: "%s"\n' "$FORWARDZONE_FILE" >> "$UNBOUND_CONF"
  fi
  if [ -f "$CONFIG_PATH" ] && ! grep -q "forward_zone_file" "$CONFIG_PATH"; then
    sed -i "s#\"blocklist_file\":#\"forward_zone_file\": \"${FORWARDZONE_FILE}\",\n  \"blocklist_file\":#" "$CONFIG_PATH"
  fi
  unbound-control reload >/dev/null 2>&1 || true
fi

CURRENT_VERSION="$("$BIN_PATH" -version 2>&1 || true)"
NEW_VERSION="$("$TMP_FILE" -version 2>&1 || true)"
if [ -n "$CURRENT_VERSION" ] || [ -n "$NEW_VERSION" ]; then
  echo "    current: ${CURRENT_VERSION:-desconhecida}"
  echo "    nova:    ${NEW_VERSION:-desconhecida}"
fi

echo "==> Backing up current binary"
cp "$BIN_PATH" "${BIN_PATH}.bak.$(date +%s)"

echo "==> Installing new binary"
install -m 0755 "$TMP_FILE" "$BIN_PATH"

echo "==> Restarting unbound-dash"
systemctl restart unbound-dash
sleep 1
systemctl is-active --quiet unbound-dash && echo "==> Done: unbound-dash atualizado e em execucao"
