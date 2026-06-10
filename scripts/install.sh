#!/usr/bin/env bash
#
# Installer for unbound-dash on Debian servers already running Unbound.
#
# Usage:
#   sudo ./install.sh [-v VERSION] [-f LOCAL_BINARY] [-p ADMIN_PASSWORD] [-l LISTEN_ADDR] [-d DOMAIN] [-e EMAIL]
#
#   -v VERSION       Release tag to download (default: latest). Requires
#                     GITHUB_TOKEN env var if the repo is private.
#   -f LOCAL_BINARY  Use a local binary instead of downloading a release.
#   -p PASSWORD      Admin password for the dashboard (prompted if omitted).
#   -l LISTEN_ADDR   Address to listen on (default: :8080). Ignored if -d is set.
#   -d DOMAIN        Public domain name pointing to this server. If set,
#                     installs nginx + certbot, obtains a Let's Encrypt
#                     certificate and serves the dashboard over HTTPS,
#                     with automatic renewal via cron.
#   -e EMAIL         Email for Let's Encrypt renewal notices (optional).
#
set -euo pipefail

REPO="airkingbr/unbound-dash"
VERSION="latest"
LOCAL_BINARY=""
ADMIN_PASSWORD=""
LISTEN_ADDR=":8080"
DOMAIN=""
CERTBOT_EMAIL=""

while getopts "v:f:p:l:d:e:h" opt; do
  case "$opt" in
    v) VERSION="$OPTARG" ;;
    f) LOCAL_BINARY="$OPTARG" ;;
    p) ADMIN_PASSWORD="$OPTARG" ;;
    l) LISTEN_ADDR="$OPTARG" ;;
    d) DOMAIN="$OPTARG" ;;
    e) CERTBOT_EMAIL="$OPTARG" ;;
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
cat > /etc/logrotate.d/unbound-dash <<EOF
${UNBOUND_LOG_FILE} {
    daily
    rotate 7
    missingok
    notifempty
    compress
    delaycompress
    copytruncate
}
EOF

echo "==> Setting up domain blocklist (anatel-blocklist.conf)"
BLOCKLIST_FILE="${CONF_D}/anatel-blocklist.conf"
if [ ! -f "$BLOCKLIST_FILE" ]; then
  printf 'server:\n' > "$BLOCKLIST_FILE"
fi
if ! grep -q "anatel-blocklist.conf" "$UNBOUND_CONF" 2>/dev/null; then
  cp "$UNBOUND_CONF" "${UNBOUND_CONF}.bak.$(date +%s)"
  printf '\ninclude: "%s"\n' "$BLOCKLIST_FILE" >> "$UNBOUND_CONF"
fi

if [ -n "$DOMAIN" ]; then
  echo "==> Configuring HTTPS (nginx + Let's Encrypt) for ${DOMAIN}"

  # The dashboard listens only on localhost; nginx terminates TLS and proxies to it.
  LISTEN_ADDR="127.0.0.1:8080"

  apt-get update -qq
  apt-get install -y nginx certbot python3-certbot-nginx

  NGINX_CONF="/etc/nginx/sites-available/unbound-dash.conf"
  cat > "$NGINX_CONF" <<EOF
server {
    listen 80;
    listen [::]:80;
    server_name ${DOMAIN};

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;

        # Needed for the live log stream (SSE)
        proxy_buffering off;
        proxy_read_timeout 1h;
    }
}
EOF
  mkdir -p /etc/nginx/sites-enabled
  ln -sf "$NGINX_CONF" /etc/nginx/sites-enabled/unbound-dash.conf
  rm -f /etc/nginx/sites-enabled/default
  nginx -t
  systemctl enable --now nginx
  systemctl reload nginx

  echo "==> Requesting Let's Encrypt certificate"
  CERTBOT_ARGS=(--nginx -d "$DOMAIN" --non-interactive --agree-tos --redirect)
  if [ -n "$CERTBOT_EMAIL" ]; then
    CERTBOT_ARGS+=(-m "$CERTBOT_EMAIL")
  else
    CERTBOT_ARGS+=(--register-unsafely-without-email)
  fi
  certbot "${CERTBOT_ARGS[@]}"

  echo "==> Configuring automatic certificate renewal (cron)"
  cat > /etc/cron.d/certbot-renew <<'EOF'
SHELL=/bin/sh
PATH=/usr/sbin:/usr/bin:/sbin:/bin
17 3,15 * * * root certbot renew --quiet --deploy-hook "systemctl reload nginx"
EOF
  chmod 644 /etc/cron.d/certbot-renew
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
  curl -fsSL -o /etc/systemd/system/unbound-dash.service \
    "https://raw.githubusercontent.com/${REPO}/main/scripts/unbound-dash.service"
fi

systemctl daemon-reload
systemctl enable --now unbound-dash

echo "==> Done"
if [ -n "$DOMAIN" ]; then
  echo "    unbound-dash is available at https://${DOMAIN}"
  echo "    (proxied by nginx, internal listener on ${LISTEN_ADDR})"
  echo "    certificate auto-renews via /etc/cron.d/certbot-renew"
else
  echo "    unbound-dash is listening on ${LISTEN_ADDR}"
fi
echo "    config: ${CONFIG_PATH}"
