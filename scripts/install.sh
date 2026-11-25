#!/usr/bin/env bash
#
# Installer for unbound-dash on Debian servers already running Unbound.
#
# Usage:
#   sudo ./install.sh [-v VERSION] [-f LOCAL_BINARY] [-p ADMIN_PASSWORD] [-l LISTEN_ADDR]
#
#   Instalacao em uma linha:
#     curl -fsSL https://raw.githubusercontent.com/airkingbr/unbound-dash/main/scripts/install.sh | sudo bash
#
#   Blocklist DNS (ISP Tools): o instalador pergunta se voce tem um token da
#   API e o valida. Sem token, a instalacao continua normalmente, apenas sem
#   aplicar bloqueios (o token pode ser colocado depois em
#   /etc/unbound-dash/config.json, campo "isptools_token").
#   Em instalacao nao interativa, passe o token pela variavel ISPTOOLS_TOKEN
#   (ex.: curl ... | sudo ISPTOOLS_TOKEN=isptbl_... bash), ou
#   ISPTOOLS_TOKEN=nenhum para pular a blocklist sem perguntar.
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

CONFIG_PATH="/etc/unbound-dash/config.json"
ISPTOOLS_API="https://isptools.gbti.net.br/api/v1/blocklist"

# have_tty: answers are read from /dev/tty even when the script itself comes
# from "curl | bash" (stdin is the pipe there). Returns 1 without a tty.
have_tty() { [ -r /dev/tty ] && [ -w /dev/tty ] && { : < /dev/tty; } 2>/dev/null; }

# check_token stores GET /versao for token $1 in TOKEN_CODE / TOKEN_BODY
# (no subshell, so both survive). The token goes to curl through stdin,
# never on the command line (where it would show up in ps).
TOKEN_CODE=""
TOKEN_BODY=""
check_token() {
  local body_file
  body_file="$(mktemp)"
  TOKEN_CODE="$(printf 'Authorization: Bearer %s\n' "$1" | curl -s -m 20 -H @- \
    -o "$body_file" -w '%{http_code}' "${ISPTOOLS_API}/versao" 2>/dev/null || true)"
  TOKEN_CODE="${TOKEN_CODE:-000}"
  TOKEN_BODY="$(cat "$body_file" 2>/dev/null || true)"
  rm -f "$body_file"
}

json_field() { printf '%s' "$TOKEN_BODY" | grep -o "\"$1\": *[^,}]*" | head -n1 | sed -E 's/^[^:]*: *"?([^"]*)"?$/\1/' || true; }

# validate_token returns 0 if the token should be saved. Explains the
# problem (401 invalid, 403 IP not allowed, network) and, when interactive,
# lets the operator decide.
validate_token() {
  local token="$1" interactive="$2" code ip answer
  if ! printf '%s' "$token" | grep -Eq '^isptbl_[0-9a-f]{48}$'; then
    echo "    token em formato invalido (esperado: isptbl_ + 48 caracteres hexadecimais)"
    return 1
  fi
  check_token "$token"
  code="$TOKEN_CODE"
  case "$code" in
    200)
      echo "    token valido: lista v$(json_field versao), $(json_field dominios) dominios"
      return 0
      ;;
    401)
      echo "    token invalido ou revogado (HTTP 401)"
      return 1
      ;;
    403)
      ip="$(curl -s -m 10 "${ISPTOOLS_API}/ip" 2>/dev/null | tr -d '[:space:]' || true)"
      echo "    o token existe, mas este servidor nao esta autorizado (HTTP 403)"
      echo "    peca a liberacao do IP ${ip:-(desconhecido)} no ISP Tools (Blocklist DNS -> Clientes e tokens)"
      ;;
    000)
      echo "    nao foi possivel falar com a API (${ISPTOOLS_API}); verifique DNS/rota/firewall de saida"
      ;;
    *)
      echo "    a API respondeu HTTP ${code}"
      ;;
  esac
  # 403 / network / other: the token may still be right.
  if [ "$interactive" = 1 ]; then
    read -r -p "    salvar o token mesmo assim? os bloqueios comecam quando a API aceitar [S/n]: " answer < /dev/tty
    case "$answer" in n|N|nao|NAO|não) return 1 ;; esac
  fi
  return 0
}

echo "==> Senha de admin do painel"
if [ -z "$ADMIN_PASSWORD" ]; then
  if [ -f "$CONFIG_PATH" ]; then
    ADMIN_PASSWORD="$(grep -o '"admin_password": *"[^"]*"' "$CONFIG_PATH" | sed -E 's/.*"([^"]*)"$/\1/')"
  fi
  if [ -z "$ADMIN_PASSWORD" ] && have_tty; then
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
    ADMIN_PASSWORD="$(tr -dc 'A-Za-z0-9' </dev/urandom 2>/dev/null | head -c 20 || true)"
    ADMIN_PASSWORD_SOURCE=generated
    echo "    senha de admin gerada (mostrada no resumo ao final)"
  fi
fi

echo "==> Blocklist DNS (ISP Tools)"
EXISTING_TOKEN=""
if [ -f "$CONFIG_PATH" ]; then
  EXISTING_TOKEN="$(grep -o '"isptools_token": *"[^"]*"' "$CONFIG_PATH" | sed -E 's/.*"([^"]*)"$/\1/' || true)"
fi

ISPTOOLS_TOKEN="${ISPTOOLS_TOKEN:-}"
case "$ISPTOOLS_TOKEN" in
  nenhum|none|no|nao)
    ISPTOOLS_TOKEN=""
    echo "    pulando a blocklist (ISPTOOLS_TOKEN=nenhum)"
    ;;
  "")
    if [ -n "$EXISTING_TOKEN" ]; then
      ISPTOOLS_TOKEN="$EXISTING_TOKEN"
      echo "    mantendo o token ja configurado em ${CONFIG_PATH}"
    elif have_tty; then
      echo "    O unbound-dash pode aplicar automaticamente a lista de bloqueios do ISP Tools"
      echo "    (oficios Anatel, Operacao 404, decisoes judiciais). Para isso e preciso um token da API."
      read -r -p "    voce tem um token da API Blocklist DNS? [s/N]: " HAS_TOKEN < /dev/tty
      case "$HAS_TOKEN" in
        s|S|sim|SIM|y|Y)
          for attempt in 1 2 3; do
            read -r -s -p "    cole o token (isptbl_...; em branco para pular): " CANDIDATE < /dev/tty
            echo
            CANDIDATE="$(printf '%s' "$CANDIDATE" | tr -d '[:space:]')"
            [ -z "$CANDIDATE" ] && break
            if validate_token "$CANDIDATE" 1; then
              ISPTOOLS_TOKEN="$CANDIDATE"
              break
            fi
            [ "$attempt" -lt 3 ] && echo "    tente novamente"
          done
          ;;
      esac
    fi
    ;;
  *)
    ISPTOOLS_TOKEN="$(printf '%s' "$ISPTOOLS_TOKEN" | tr -d '[:space:]')"
    if ! validate_token "$ISPTOOLS_TOKEN" 0; then
      echo "    token de ISPTOOLS_TOKEN rejeitado; instalando sem blocklist" >&2
      ISPTOOLS_TOKEN=""
    fi
    ;;
esac
if [ -z "$ISPTOOLS_TOKEN" ]; then
  echo "    sem token: a instalacao continua, mas nenhum bloqueio sera aplicado."
  echo "    para ativar depois: coloque o token em \"isptools_token\" no ${CONFIG_PATH}"
  echo "    e rode: systemctl restart unbound-dash"
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
# Uses create+postrotate (rename + new file + log_reopen) instead of
# copytruncate: copytruncate needs free space equal to the log size to
# make a copy before truncating -- on a full disk it silently fails and
# the log keeps growing.
cat > /etc/logrotate.d/unbound-dash <<EOF
${UNBOUND_LOG_FILE} {
    size 200M
    rotate 4
    missingok
    notifempty
    compress
    delaycompress
    dateext
    create 640 unbound adm
    postrotate
        /usr/sbin/unbound-control log_reopen 2>/dev/null || true
    endscript
}
EOF

cat > /etc/cron.hourly/unbound-dash-logrotate <<EOF
#!/bin/sh
exec /usr/sbin/logrotate /etc/logrotate.d/unbound-dash
EOF
chmod 0755 /etc/cron.hourly/unbound-dash-logrotate

# The DNS blocklist (isptools-blocklist.conf) is created and included by
# unbound-dash itself on the first successful sync with the ISP Tools API.

echo "==> Setting up forward zones (forwardzone.conf)"
FORWARDZONE_FILE="${CONF_D}/forwardzone.conf"
if [ ! -f "$FORWARDZONE_FILE" ]; then
  : > "$FORWARDZONE_FILE"
fi
if ! grep -q "forwardzone.conf" "$UNBOUND_CONF" 2>/dev/null; then
  cp "$UNBOUND_CONF" "${UNBOUND_CONF}.bak.$(date +%s)"
  printf '\ninclude: "%s"\n' "$FORWARDZONE_FILE" >> "$UNBOUND_CONF"
fi

echo "==> Setting up static entries (staticentries.conf)"
STATICENTRY_FILE="${CONF_D}/staticentries.conf"
if [ ! -f "$STATICENTRY_FILE" ]; then
  : > "$STATICENTRY_FILE"
fi
if ! grep -q "staticentries.conf" "$UNBOUND_CONF" 2>/dev/null; then
  cp "$UNBOUND_CONF" "${UNBOUND_CONF}.bak.$(date +%s)"
  printf '\ninclude: "%s"\n' "$STATICENTRY_FILE" >> "$UNBOUND_CONF"
fi

echo "==> Writing config"
mkdir -p /etc/unbound-dash


UNBOUND_CONTROL_BIN="$(command -v unbound-control)"
UNBOUND_CHECKCONF_BIN="$(command -v unbound-checkconf || echo /usr/sbin/unbound-checkconf)"

GENERATED_PASSWORD=0
case "${ADMIN_PASSWORD_SOURCE:-}" in generated) GENERATED_PASSWORD=1 ;; esac

umask 077
cat > "$CONFIG_PATH" <<EOF
{
  "listen_addr": "${LISTEN_ADDR}",
  "unbound_control_bin": "${UNBOUND_CONTROL_BIN}",
  "unbound_checkconf_bin": "${UNBOUND_CHECKCONF_BIN}",
  "unbound_conf": "${UNBOUND_CONF}",
  "unbound_log_file": "${UNBOUND_LOG_FILE}",
  "forward_zone_file": "${FORWARDZONE_FILE}",
  "static_entry_file": "${STATICENTRY_FILE}",
  "isptools_token": "${ISPTOOLS_TOKEN}",
  "admin_password": "${ADMIN_PASSWORD}"
}
EOF
chmod 600 "$CONFIG_PATH"
umask 022

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
INSTALL_START="$(date '+%Y-%m-%d %H:%M:%S')"
systemctl enable unbound-dash >/dev/null 2>&1
systemctl restart unbound-dash

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

BLOCKLIST_RESULT="desativada (sem token)"
if [ -n "$ISPTOOLS_TOKEN" ]; then
  echo "==> Aguardando a primeira sincronizacao da blocklist (ate 3 min)"
  BLOCKLIST_RESULT="token configurado; sincronizacao ainda nao concluiu (veja a aba Bloqueios)"
  for _ in $(seq 1 36); do
    sleep 5
    line="$(journalctl -u unbound-dash --since "$INSTALL_START" --no-pager -o cat 2>/dev/null \
      | grep -E 'isptools: (aplicada|sincronizacao falhou)' | tail -n1 || true)"
    case "$line" in
      *aplicada*) BLOCKLIST_RESULT="ativa — ${line#*isptools: }"; break ;;
      *falhou*) BLOCKLIST_RESULT="erro — ${line#*isptools: }"; break ;;
    esac
    # Reinstall on a server already in sync: nothing new to apply.
    if [ -s /var/lib/unbound-dash/isptools-domains.txt ] && \
       grep -q '"applied_md5": *"[a-f0-9]\{32\}"' /var/lib/unbound-dash/isptools-state.json 2>/dev/null; then
      BLOCKLIST_RESULT="ativa — $(wc -l < /var/lib/unbound-dash/isptools-domains.txt | tr -d ' ') dominios aplicados"
      break
    fi
  done
fi

SERVER_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
PORT="${LISTEN_ADDR##*:}"

echo
echo "=================================================================="
echo " unbound-dash instalado"
echo "=================================================================="
echo " Painel:     http://${SERVER_IP:-SEU_SERVIDOR}:${PORT:-8080}"
if [ "$GENERATED_PASSWORD" = 1 ]; then
  echo " Senha:      ${ADMIN_PASSWORD}   (gerada agora; guarde-a)"
else
  echo " Senha:      a informada/ja configurada"
fi
echo " Blocklist:  ${BLOCKLIST_RESULT}"
echo " Config:     ${CONFIG_PATH}"
echo " Atualizar:  sudo update-unbound-dash"
echo "=================================================================="
if [ -z "$ISPTOOLS_TOKEN" ]; then
  echo " Para ativar a blocklist depois: coloque o token em \"isptools_token\""
  echo " no ${CONFIG_PATH} e rode: systemctl restart unbound-dash"
fi
