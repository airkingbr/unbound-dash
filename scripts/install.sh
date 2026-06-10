#!/usr/bin/env bash
#
# Installer for unbound-dash on Debian servers already running Unbound.
#
# Usage:
#   sudo ./install.sh [-v VERSION] [-f LOCAL_BINARY] [-p ADMIN_PASSWORD] [-l LISTEN_ADDR]
#
#   -v VERSION       Release tag to download (default: latest). Requires
#                     GITHUB_TOKEN env var if the repo is private.
#   -f LOCAL_BINARY  Use a local binary instead of downloading a release.
#   -p PASSWORD      Admin password for the dashboard (prompted if omitted).
#   -l LISTEN_ADDR   Address to listen on (default: :8080).
#
set -euo pipefail

REPO="airkingbr/unbound-dash"
VERSION="latest"
LOCAL_BINARY=""
ADMIN_PASSWORD=""
LISTEN_ADDR=":8080"

while getopts "v:f:p:l:h" opt; do
  case "$opt" in
    v) VERSION="$OPTARG" ;;
    f) LOCAL_BINARY="$OPTARG" ;;
    p) ADMIN_PASSWORD="$OPTARG" ;;
    l) LISTEN_ADDR="$OPTARG" ;;
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

if ! command -v unbound-control >/dev/null 2>&1; then
  echo "unbound-control not found; is Unbound installed?" >&2
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

echo "==> Installing unbound-dash binary"
if [ -n "$LOCAL_BINARY" ]; then
  install -m 0755 "$LOCAL_BINARY" "$BIN_PATH"
else
  CURL_AUTH=()
  if [ -n "${GITHUB_TOKEN:-}" ]; then
    CURL_AUTH=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
  fi

  if [ "$VERSION" = "latest" ]; then
    API_URL="https://api.github.com/repos/${REPO}/releases/latest"
  else
    API_URL="https://api.github.com/repos/${REPO}/releases/tags/${VERSION}"
  fi

  ASSET_NAME="unbound-dash_linux_${GOARCH}"
  DOWNLOAD_URL="$(curl -fsSL "${CURL_AUTH[@]}" "$API_URL" \
    | grep -o "\"browser_download_url\": *\"[^\"]*${ASSET_NAME}[^\"]*\"" \
    | head -n1 | sed -E 's/.*"(https[^"]+)"/\1/')"

  if [ -z "$DOWNLOAD_URL" ]; then
    echo "could not find a release asset matching ${ASSET_NAME} (version: ${VERSION})" >&2
    echo "pass -f LOCAL_BINARY to install from a local build instead" >&2
    exit 1
  fi

  TMP_FILE="$(mktemp)"
  trap 'rm -f "$TMP_FILE"' EXIT
  curl -fsSL "${CURL_AUTH[@]}" -o "$TMP_FILE" "$DOWNLOAD_URL"
  install -m 0755 "$TMP_FILE" "$BIN_PATH"
fi

echo "==> Checking unbound-control configuration"
UNBOUND_CONF="/etc/unbound/unbound.conf"
if ! unbound-control status >/dev/null 2>&1; then
  echo "    remote-control not enabled or not reachable; enabling it"
  CONF_D="/etc/unbound/unbound.conf.d"
  mkdir -p "$CONF_D"
  cat > "${CONF_D}/99-unbound-dash-control.conf" <<'EOF'
remote-control:
    control-enable: yes
    control-interface: 127.0.0.1
    control-port: 8953
EOF
  if ! grep -q "include:.*unbound.conf.d" "$UNBOUND_CONF" 2>/dev/null; then
    cp "$UNBOUND_CONF" "${UNBOUND_CONF}.bak.$(date +%s)"
    printf '\ninclude: "%s/*.conf"\n' "$CONF_D" >> "$UNBOUND_CONF"
  fi
  unbound-control-setup
  systemctl restart unbound
  sleep 1
  unbound-control status >/dev/null
fi

echo "==> Writing config"
mkdir -p /etc/unbound-dash
CONFIG_PATH="/etc/unbound-dash/config.json"

if [ -z "$ADMIN_PASSWORD" ]; then
  if [ -f "$CONFIG_PATH" ]; then
    ADMIN_PASSWORD="$(grep -o '"admin_password": *"[^"]*"' "$CONFIG_PATH" | sed -E 's/.*"([^"]*)"$/\1/')"
  fi
  if [ -z "$ADMIN_PASSWORD" ] && [ -r /dev/tty ]; then
    read -r -s -p "    admin password (deixe em branco para gerar uma aleatoria): " ADMIN_PASSWORD < /dev/tty
    echo
    if [ -n "$ADMIN_PASSWORD" ]; then
      read -r -s -p "    confirme a senha: " ADMIN_PASSWORD_CONFIRM < /dev/tty
      echo
      if [ "$ADMIN_PASSWORD" != "$ADMIN_PASSWORD_CONFIRM" ]; then
        echo "as senhas nao coincidem" >&2
        exit 1
      fi
    fi
  fi
  if [ -z "$ADMIN_PASSWORD" ]; then
    ADMIN_PASSWORD="$(tr -dc 'A-Za-z0-9' </dev/urandom | head -c 20)"
    echo "    generated admin password: ${ADMIN_PASSWORD}"
  fi
fi

UNBOUND_CONTROL_BIN="$(command -v unbound-control)"

cat > "$CONFIG_PATH" <<EOF
{
  "listen_addr": "${LISTEN_ADDR}",
  "unbound_control_bin": "${UNBOUND_CONTROL_BIN}",
  "unbound_conf": "${UNBOUND_CONF}",
  "admin_password": "${ADMIN_PASSWORD}"
}
EOF
chmod 600 "$CONFIG_PATH"

echo "==> Installing systemd service"
SCRIPT_DIR=""
if [ -n "${BASH_SOURCE:-}" ]; then
  SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
fi
if [ -f "${SCRIPT_DIR}/unbound-dash.service" ]; then
  cp "${SCRIPT_DIR}/unbound-dash.service" /etc/systemd/system/unbound-dash.service
else
  curl -fsSL -o /etc/systemd/system/unbound-dash.service \
    "https://raw.githubusercontent.com/${REPO}/main/scripts/unbound-dash.service"
fi

systemctl daemon-reload
systemctl enable --now unbound-dash

echo "==> Done"
echo "    unbound-dash is listening on ${LISTEN_ADDR}"
echo "    config: ${CONFIG_PATH}"
