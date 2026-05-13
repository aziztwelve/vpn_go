#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# prepare-vps.sh — bootstrap нового Xray-VPS (запускается на удалённой машине)
#
# Что делает:
#   1. Ставит Docker (если нет).
#   2. Настраивает iptables (IPv4): открывает :XRAY_LISTEN_PORT, оставляет :10085
#      только для backend (по IPv4).
#   3. Настраивает ip6tables (IPv6): :10085 закрыт глобально (lo разрешён). Если
#      BACKEND_IP6 задан — ACCEPT и для него по IPv6. По умолчанию у нашего
#      backend нет IPv6, поэтому policy = DROP all v6 на :10085.
#   4. Устанавливает iptables-persistent и сохраняет правила (и v4, и v6).
#
# Использование (с локальной машины-агента):
#   ssh root@<IP> 'bash -s' < add_server/prepare-vps.sh
#   # или с переопределением:
#   ssh root@<IP> 'BACKEND_IP6=2a01:... bash -s' < add_server/prepare-vps.sh
#
# История: до 2026-05-07 скрипт настраивал только iptables (IPv4) — и :10085
# на новых VPS оставался открыт по IPv6. Симптом: внешний probe `nc -6 -zv`
# на :10085 проходил, что эквивалентно открытому Xray gRPC API в интернет.
# Хотфикс был применён вручную на проде, теперь зашит в скрипт.
# -----------------------------------------------------------------------------
set -euo pipefail

BACKEND_IP="${BACKEND_IP:-178.104.217.201}"
BACKEND_IP6="${BACKEND_IP6:-}"   # пусто = backend без IPv6, ничего не whitelist-им
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

ok "iptables (v4) правила применены"

# ---------- 4. ip6tables (IPv6) — закрыть :XRAY_API_PORT ----------
# До 2026-05-07 этого блока не было, и :10085 оставался открыт по IPv6.
# Логика симметрична v4: lo всегда ACCEPT, IPv6-backend (если задан) ACCEPT,
# всё остальное на :10085 — DROP. Идемпотентно.
log "Настройка ip6tables (IPv6 :$XRAY_API_PORT lockdown)…"

# Проверим что ip6tables вообще доступен (на VPS без IPv6-стека он может быть
# отсутствующим, но обычно есть). Если нет — пропустим с warning, не валимся.
if ! command -v ip6tables >/dev/null 2>&1; then
  err "ip6tables не найден — пропускаем IPv6 lockdown. Если у VPS есть IPv6, :$XRAY_API_PORT останется открыт!"
else
  # Чистим возможные дубли от предыдущего прогона.
  for r in \
    "-p tcp --dport $XRAY_API_PORT -i lo -j ACCEPT" \
    "-p tcp --dport $XRAY_API_PORT -j DROP"; do
    while ip6tables -C INPUT $r 2>/dev/null; do
      ip6tables -D INPUT $r
    done
  done
  if [ -n "$BACKEND_IP6" ]; then
    while ip6tables -C INPUT -p tcp --dport "$XRAY_API_PORT" -s "$BACKEND_IP6" -j ACCEPT 2>/dev/null; do
      ip6tables -D INPUT -p tcp --dport "$XRAY_API_PORT" -s "$BACKEND_IP6" -j ACCEPT
    done
  fi

  # Применяем (порядок важен).
  ip6tables -I INPUT -p tcp --dport "$XRAY_API_PORT" -i lo -j ACCEPT
  if [ -n "$BACKEND_IP6" ]; then
    ip6tables -I INPUT 2 -p tcp --dport "$XRAY_API_PORT" -s "$BACKEND_IP6" -j ACCEPT
    ok "ip6tables: backend IPv6 $BACKEND_IP6 whitelisted"
  fi
  ip6tables -A INPUT -p tcp --dport "$XRAY_API_PORT" -j DROP

  # И для DOCKER-USER — Docker создаёт её и для v6 если daemon с ipv6=true,
  # но безопаснее проверить наличие цепочки.
  if ip6tables -L DOCKER-USER -n >/dev/null 2>&1; then
    ip6tables -F DOCKER-USER || true
    if [ -n "$BACKEND_IP6" ]; then
      ip6tables -A DOCKER-USER -p tcp --dport "$XRAY_API_PORT" -s "$BACKEND_IP6" -j ACCEPT
    fi
    ip6tables -A DOCKER-USER -p tcp --dport "$XRAY_API_PORT" -j DROP
    ok "ip6tables DOCKER-USER chain настроена"
  else
    log "ip6tables DOCKER-USER chain отсутствует (Docker без ipv6) — это нормально"
  fi

  ok "ip6tables (v6) правила применены"
fi

# ---------- 5. Сохранение правил v4+v6 ----------
netfilter-persistent save >/dev/null
ok "iptables-persistent сохранил правила (v4 + v6)"

# ---------- 6. Резюме ----------
echo
log "DOCKER-USER chain (ожидаемо: 1=ACCEPT $BACKEND_IP, 2=DROP)"
iptables -L DOCKER-USER -n --line-numbers

echo
log "INPUT chain v4 (бэкап для будущих изменений)"
iptables -L INPUT -n --line-numbers | grep -E "($XRAY_API_PORT|^Chain|^num)"

if command -v ip6tables >/dev/null 2>&1; then
  echo
  log "INPUT chain v6 (должен содержать DROP на :$XRAY_API_PORT)"
  ip6tables -L INPUT -n --line-numbers | grep -E "($XRAY_API_PORT|^Chain|^num)"
fi

echo
ok "VPS готов. Дальше: scp /opt/xray/config.json + docker run xray (см. add_server/README.md шаг 4)."
