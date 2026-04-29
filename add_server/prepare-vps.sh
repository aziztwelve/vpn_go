#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# prepare-vps.sh — bootstrap нового Xray-VPS (запускается на удалённой машине)
#
# Что делает:
#   1. Ставит Docker (если нет).
#   2. Настраивает iptables: открывает :443, оставляет :10085 только для backend.
#   3. Устанавливает iptables-persistent и сохраняет правила.
#
# Использование (с локальной машины-агента):
#   ssh root@<IP> 'bash -s' < add_server/prepare-vps.sh
#
# Если backend поменяет IP — поправь BACKEND_IP ниже и пере-прогони.
# -----------------------------------------------------------------------------
set -euo pipefail

BACKEND_IP="${BACKEND_IP:-178.104.217.201}"
XRAY_API_PORT="${XRAY_API_PORT:-10085}"
XRAY_LISTEN_PORT="${XRAY_LISTEN_PORT:-443}"

log() { printf "\033[1;36m==>\033[0m %s\n" "$*"; }
ok()  { printf "\033[1;32m✅\033[0m %s\n" "$*"; }
err() { printf "\033[1;31m❌\033[0m %s\n" "$*" >&2; }

if [ "$(id -u)" -ne 0 ]; then
  err "Запускай от root."
  exit 1
fi

log "Hostname / OS / arch:"
hostname
uname -m
. /etc/os-release && echo "$NAME $VERSION_ID"

# ---------- 1. Docker ----------
if ! command -v docker >/dev/null 2>&1; then
  log "Установка Docker через get.docker.com…"
  curl -fsSL https://get.docker.com | sh
else
  ok "Docker уже стоит: $(docker --version)"
fi

systemctl enable --now docker >/dev/null 2>&1 || true
docker --version
ok "Docker active: $(systemctl is-active docker)"

# ---------- 2. iptables-persistent ----------
if ! dpkg -l iptables-persistent 2>/dev/null | grep -q '^ii'; then
  log "Установка iptables-persistent…"
  DEBIAN_FRONTEND=noninteractive apt-get update -qq >/dev/null
  echo iptables-persistent iptables-persistent/autosave_v4 boolean false | debconf-set-selections
  echo iptables-persistent iptables-persistent/autosave_v6 boolean false | debconf-set-selections
  DEBIAN_FRONTEND=noninteractive apt-get install -y iptables-persistent >/dev/null
fi
ok "iptables-persistent готов"

# ---------- 3. Firewall ----------
log "Настройка iptables…"

# DOCKER-USER гарантированно существует после установки Docker (Docker сам её создаёт).
# Чистим, чтобы повторный прогон не множил правила.
iptables -F DOCKER-USER || true

# ВАЖЕН ПОРЯДОК: ACCEPT для backend → DROP catch-all. Используем -A (append).
iptables -A DOCKER-USER -p tcp --dport "$XRAY_API_PORT" -s "$BACKEND_IP" -j ACCEPT
iptables -A DOCKER-USER -p tcp --dport "$XRAY_API_PORT" -j DROP

# Подстраховка для INPUT chain — если когда-то включат userland-proxy.
# Идемпотентно: удаляем потенциальные старые правила и добавляем заново.
for r in \
  "-p tcp --dport $XRAY_API_PORT -i lo -j ACCEPT" \
  "-p tcp --dport $XRAY_API_PORT -s $BACKEND_IP -j ACCEPT" \
  "-p tcp --dport $XRAY_API_PORT -j DROP"; do
  while iptables -C INPUT $r 2>/dev/null; do
    iptables -D INPUT $r
  done
done
iptables -I INPUT -p tcp --dport "$XRAY_API_PORT" -i lo -j ACCEPT
iptables -I INPUT 2 -p tcp --dport "$XRAY_API_PORT" -s "$BACKEND_IP" -j ACCEPT
iptables -A INPUT -p tcp --dport "$XRAY_API_PORT" -j DROP

netfilter-persistent save >/dev/null
ok "iptables правила применены и сохранены"

# ---------- 4. Резюме ----------
echo
log "DOCKER-USER chain (ожидаемо: 1=ACCEPT $BACKEND_IP, 2=DROP)"
iptables -L DOCKER-USER -n --line-numbers

echo
log "INPUT chain (бэкап для будущих изменений)"
iptables -L INPUT -n --line-numbers | grep -E "($XRAY_API_PORT|^Chain|^num)"

echo
ok "VPS готов. Дальше: scp /opt/xray/config.json + docker run xray (см. add_server/README.md шаг 4)."
