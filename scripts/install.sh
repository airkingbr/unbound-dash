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

if ! command -v pdftotext >/dev/null 2>&1; then
  echo "==> Installing poppler-utils (pdftotext, usado para importar oficios em PDF)"
  apt-get update -qq
  apt-get install -y poppler-utils
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
  ASSET_ID="$(echo "$RELEASE_JSON" | jq -r --arg name "$ASSET_NAME" '.assets[] | select(.name == $name) | .id' | head -n1)"

  if [ -z "$ASSET_ID" ] || [ "$ASSET_ID" = "null" ]; then
    echo "could not find a release asset matching ${ASSET_NAME} (version: ${VERSION})" >&2
    echo "pass -f LOCAL_BINARY to install from a local build instead" >&2
    exit 1
  fi

  TMP_FILE="$(mktemp)"
  trap 'rm -f "$TMP_FILE"' EXIT
  curl -fsSL "${CURL_AUTH[@]}" -H "Accept: application/octet-stream" \
    -o "$TMP_FILE" "https://api.github.com/repos/${REPO}/releases/assets/${ASSET_ID}"
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
  if ! grep -q "99-unbound-dash-control.conf" "$UNBOUND_CONF" 2>/dev/null; then
    cp "$UNBOUND_CONF" "${UNBOUND_CONF}.bak.$(date +%s)"
    printf '\ninclude: "%s/99-unbound-dash-control.conf"\n' "$CONF_D" >> "$UNBOUND_CONF"
  fi
  unbound-control-setup
  systemctl restart unbound
  sleep 1
  unbound-control status >/dev/null
fi

echo "==> Enabling Unbound query log (for top domains/clients stats)"
CONF_D="/etc/unbound/unbound.conf.d"
mkdir -p "$CONF_D"
if [ ! -f "${CONF_D}/99-unbound-dash-querylog.conf" ]; then
  cat > "${CONF_D}/99-unbound-dash-querylog.conf" <<'EOF'
server:
    log-queries: yes
EOF
  if ! grep -q "99-unbound-dash-querylog.conf" "$UNBOUND_CONF" 2>/dev/null; then
    cp "$UNBOUND_CONF" "${UNBOUND_CONF}.bak.$(date +%s)"
    printf '\ninclude: "%s/99-unbound-dash-querylog.conf"\n' "$CONF_D" >> "$UNBOUND_CONF"
  fi
  unbound-control set_option log-queries: yes >/dev/null 2>&1 || true
fi

UNBOUND_LOG_FILE="$(unbound-control get_option logfile 2>/dev/null | tr -d '[:space:]')"
if [ -z "$UNBOUND_LOG_FILE" ]; then
  UNBOUND_LOG_FILE="/var/log/unbound/unbound.log"
fi

echo "==> Configuring log rotation for ${UNBOUND_LOG_FILE}"
# Rotated by size (not just daily) and checked hourly: with log-queries
# enabled, query volume can fill the disk in days if rotation only runs
# once a day, which then breaks unrelated things (e.g. Unbound failing to
# rewrite /var/lib/unbound/root.key when the disk is full).
cat > /etc/logrotate.d/unbound-dash <<EOF
${UNBOUND_LOG_FILE} {
    size 200M
    rotate 4
    missingok
    notifempty
    compress
    delaycompress
    copytruncate
    dateext
}
EOF

cat > /etc/cron.hourly/unbound-dash-logrotate <<EOF
#!/bin/sh
exec /usr/sbin/logrotate /etc/logrotate.d/unbound-dash
EOF
chmod 0755 /etc/cron.hourly/unbound-dash-logrotate

echo "==> Setting up domain blocklist (anatel-blocklist.conf)"
BLOCKLIST_FILE="${CONF_D}/anatel-blocklist.conf"
if [ ! -f "$BLOCKLIST_FILE" ]; then
  printf 'server:\n' > "$BLOCKLIST_FILE"
fi
if ! grep -q "anatel-blocklist.conf" "$UNBOUND_CONF" 2>/dev/null; then
  cp "$UNBOUND_CONF" "${UNBOUND_CONF}.bak.$(date +%s)"
  printf '\ninclude: "%s"\n' "$BLOCKLIST_FILE" >> "$UNBOUND_CONF"
fi

echo "==> Setting up forward zones (forwardzone.conf)"
FORWARDZONE_FILE="${CONF_D}/forwardzone.conf"
if [ ! -f "$FORWARDZONE_FILE" ]; then
  : > "$FORWARDZONE_FILE"
fi
if ! grep -q "forwardzone.conf" "$UNBOUND_CONF" 2>/dev/null; then
  cp "$UNBOUND_CONF" "${UNBOUND_CONF}.bak.$(date +%s)"
  printf '\ninclude: "%s"\n' "$FORWARDZONE_FILE" >> "$UNBOUND_CONF"
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
PDFTOTEXT_BIN="$(command -v pdftotext || echo pdftotext)"

cat > "$CONFIG_PATH" <<EOF
{
  "listen_addr": "${LISTEN_ADDR}",
  "unbound_control_bin": "${UNBOUND_CONTROL_BIN}",
  "unbound_conf": "${UNBOUND_CONF}",
  "unbound_log_file": "${UNBOUND_LOG_FILE}",
  "blocklist_file": "${BLOCKLIST_FILE}",
  "forward_zone_file": "${FORWARDZONE_FILE}",
  "pdftotext_bin": "${PDFTOTEXT_BIN}",
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
  CURL_AUTH=()
  if [ -n "${GITHUB_TOKEN:-}" ]; then
    CURL_AUTH=(-H "Authorization: token ${GITHUB_TOKEN}")
  fi
  curl -fsSL "${CURL_AUTH[@]}" -H "Accept: application/vnd.github.raw" \
    -o /etc/systemd/system/unbound-dash.service \
    "https://api.github.com/repos/${REPO}/contents/scripts/unbound-dash.service"
fi

systemctl daemon-reload
systemctl enable --now unbound-dash

echo "==> Installing update-unbound-dash"
UPDATE_BIN="/usr/local/bin/update-unbound-dash"
if [ -f "${SCRIPT_DIR}/update.sh" ]; then
  install -m 0755 "${SCRIPT_DIR}/update.sh" "$UPDATE_BIN"
else
  CURL_AUTH=()
  if [ -n "${GITHUB_TOKEN:-}" ]; then
    CURL_AUTH=(-H "Authorization: token ${GITHUB_TOKEN}")
  fi
  curl -fsSL "${CURL_AUTH[@]}" -H "Accept: application/vnd.github.raw" \
    -o "$UPDATE_BIN" \
    "https://api.github.com/repos/${REPO}/contents/scripts/update.sh"
  chmod 0755 "$UPDATE_BIN"
fi

echo "==> Done"
echo "    unbound-dash is listening on ${LISTEN_ADDR}"
echo "    config: ${CONFIG_PATH}"
echo "    para atualizar: sudo update-unbound-dash"
