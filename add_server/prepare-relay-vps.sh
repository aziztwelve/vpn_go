#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# prepare-relay-vps.sh — bootstrap RU-relay узла для каскадной схемы.
#
# Чем отличается от prepare-vps.sh:
#   - НЕТ gRPC Xray API на :10085 (relay-узел не управляется бэкендом,
#     он лишь переливает байты в exit-узел через dokodemo-door).
#   - Открыт только :22 (SSH) и :8443 (Reality forward).
#   - Не нужно whitelist'ить backend-IP — relay-узел ничего не отдаёт
#     наружу кроме TCP-коннектов от клиентов.
#
# Что делает:
#   1. Ставит Docker (если нет).
#   2. Настраивает iptables: открыт :22 и :8443, остальное закрыто.
#   3. Ставит iptables-persistent и сохраняет правила.
#
# Использование (с локальной машины-агента):
#   ssh root@<RU_IP> 'bash -s' < add_server/prepare-relay-vps.sh
#
# Дальше — см. add_server/README-relay.md шаг 3 (deploy xray dokodemo).
# -----------------------------------------------------------------------------
set -euo pipefail

XRAY_LISTEN_PORT="${XRAY_LISTEN_PORT:-8443}"

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

# DOCKER-USER — Docker сам её создаёт при установке.
# Чистим, чтобы повторный прогон не множил правила.
iptables -F DOCKER-USER || true

# Relay не имеет management-портов: пускаем только :8443 (Reality forward)
# для всех. Catch-all DROP на остальные порты Docker-контейнеров.
# Сейчас контейнер xray слушает только :8443, так что catch-all DROP
# для других портов — про запас (если в будущем добавят что-то ещё).
iptables -A DOCKER-USER -p tcp --dport "$XRAY_LISTEN_PORT" -j ACCEPT
iptables -A DOCKER-USER -p tcp -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
# Не делаем глобальный DROP — оставляем дефолтный RETURN (Docker сам решит).
# Этого достаточно: единственный публикуемый порт — :8443 — уже разрешён выше.

# INPUT — на случай userland-proxy или сервисов на хосте.
# Идемпотентно: чистим старые правила и добавляем заново.
for r in \
  "-p tcp --dport $XRAY_LISTEN_PORT -j ACCEPT"; do
  while iptables -C INPUT $r 2>/dev/null; do
    iptables -D INPUT $r
  done
done
iptables -I INPUT -p tcp --dport "$XRAY_LISTEN_PORT" -j ACCEPT

netfilter-persistent save >/dev/null
ok "iptables правила применены и сохранены"

# ---------- 4. Резюме ----------
echo
log "DOCKER-USER chain (ожидаемо: ACCEPT :$XRAY_LISTEN_PORT)"
iptables -L DOCKER-USER -n --line-numbers

echo
log "INPUT chain (порт :$XRAY_LISTEN_PORT)"
iptables -L INPUT -n --line-numbers | grep -E "($XRAY_LISTEN_PORT|^Chain|^num)"

echo
ok "Relay-VPS готов. Дальше: scp /opt/xray/config.json + docker run xray (см. add_server/README-relay.md шаг 3)."
