#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# cascade-watch.sh — мониторинг каскадных серверов (relay-через-РФ).
#
# Что показывает:
#   - Состояние самого relay (ping + TCP probe + xray-контейнер)
#   - Кол-во юзеров, подключившихся через каскад за последние периоды
#   - Сколько байт прокачано (тест-байты vs реальное использование)
#   - Сравнение трафика прямой Финляндии vs каскадной
#
# Использование:
#   ./deploy/scripts/cascade-watch.sh                        # все каскадные сервера
#   ./deploy/scripts/cascade-watch.sh --id 142               # один сервер по ID
#   ./deploy/scripts/cascade-watch.sh --threshold 10         # выйти exit 1 если юзеров < 10
#                                                              (для использования в cron / алертах)
#
# Каскадный сервер опознаётся по тому что host != xray_api_host
# (на нём «снаружи» один IP, а управление xray идёт на другой).
# -----------------------------------------------------------------------------
set -euo pipefail

SERVER_ID=""
THRESHOLD=0  # порог алерта: если юзеров <THRESHOLD за 24ч → exit 1
while [[ $# -gt 0 ]]; do
  case "$1" in
    --id) SERVER_ID="$2"; shift 2 ;;
    --threshold) THRESHOLD="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

PSQL="docker exec -i vpn-postgres psql -U vpn -d vpn"

bold()  { printf "\033[1m%s\033[0m\n" "$*"; }
ok()    { printf "  \033[1;32m✅ %s\033[0m\n" "$*"; }
warn()  { printf "  \033[1;33m⚠️  %s\033[0m\n" "$*"; }
err()   { printf "  \033[1;31m❌ %s\033[0m\n" "$*"; }
hr()    { printf "\033[2m──────────────────────────────────────────────────────────────────\033[0m\n"; }

# Найти все каскадные сервера: host != xray_api_host И ОБА являются IPv4
# (нужно исключить локальные xray где xray_api_host='xray' — это docker DNS,
# не настоящий относительный IP). Если задан --id — фильтруем по нему.
IPV4_REGEX='^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$'
QUERY="
  SELECT id, name, country_code, host, port, xray_api_host, server_max_connections
  FROM vpn_servers
  WHERE is_active = TRUE
    AND host <> xray_api_host
    AND host          ~ '$IPV4_REGEX'
    AND xray_api_host ~ '$IPV4_REGEX'
    $([ -n "$SERVER_ID" ] && echo "AND id = $SERVER_ID" || echo "")
  ORDER BY id;
"

mapfile -t ROWS < <($PSQL -At -F$'\t' -c "$QUERY")

if [ ${#ROWS[@]} -eq 0 ]; then
  warn "Каскадные серверы не найдены (или указанный --id не существует / не is_active)."
  exit 0
fi

bold "🌉 Cascade Watch — $(date '+%Y-%m-%d %H:%M:%S %Z')"
hr

EXIT_CODE=0
for ROW in "${ROWS[@]}"; do
  IFS=$'\t' read -r ID NAME CC HOST PORT API_HOST MAX_CONN <<< "$ROW"

  bold "🔗 [$ID] $NAME — relay $HOST:$PORT → exit $API_HOST"

  # 1. TCP probe до relay
  if timeout 3 bash -c "</dev/tcp/$HOST/$PORT" 2>/dev/null; then
    ok "TCP $HOST:$PORT доступен"
  else
    err "TCP $HOST:$PORT НЕ ДОСТУПЕН — relay упал?"
    EXIT_CODE=1
  fi

  # 2. RTT relay → exit (через ssh, only если ключ есть)
  if ssh -o BatchMode=yes -o ConnectTimeout=5 root@"$HOST" 'true' 2>/dev/null; then
    RTT=$(ssh root@"$HOST" "ping -c 3 -q $API_HOST 2>/dev/null | tail -1 | grep -oP 'avg/[^/]*' | cut -d= -f1 | head -c 0; ping -c 3 -q $API_HOST 2>/dev/null | tail -1 | awk -F'/' '{print \$5}'" || echo "?")
    if [ -n "$RTT" ] && [ "$RTT" != "?" ]; then
      printf "  📡 RTT relay→exit: \033[36m%s ms\033[0m\n" "$RTT"
    fi

    # 3. xray-контейнер на relay
    XRAY_STATUS=$(ssh root@"$HOST" "docker inspect xray --format '{{.State.Status}}' 2>/dev/null" || echo "no-container")
    if [ "$XRAY_STATUS" = "running" ]; then
      ok "xray container running"
    else
      err "xray container: $XRAY_STATUS"
      EXIT_CODE=1
    fi
  else
    warn "SSH-доступ к $HOST не настроен — пропускаю проверку relay-side"
  fi

  # 4. Активность (по traffic_samples)
  STATS=$($PSQL -At -F'|' -c "
    SELECT
      COUNT(DISTINCT vpn_user_id) FILTER (WHERE collected_at >= NOW() - INTERVAL '5 min'),
      COUNT(DISTINCT vpn_user_id) FILTER (WHERE collected_at >= NOW() - INTERVAL '1 hour'),
      COUNT(DISTINCT vpn_user_id) FILTER (WHERE collected_at >= NOW() - INTERVAL '24 hours'),
      COUNT(DISTINCT vpn_user_id) FILTER (WHERE collected_at >= NOW() - INTERVAL '7 days'),
      pg_size_pretty(COALESCE(SUM(uplink_bytes+downlink_bytes) FILTER (WHERE collected_at >= NOW() - INTERVAL '24 hours'), 0))
    FROM traffic_samples WHERE server_id = $ID;
  ")
  IFS='|' read -r U5 U1H U24 U7D BYTES_24H <<< "$STATS"

  printf "  👥 Юзеров: 5min=\033[1m%s\033[0m  1h=\033[1m%s\033[0m  24h=\033[1m%s\033[0m  7d=\033[1m%s\033[0m\n" "$U5" "$U1H" "$U24" "$U7D"
  printf "  📦 Трафик (24ч): \033[1m%s\033[0m\n" "$BYTES_24H"

  # Алерт-логика: если threshold задан и юзеров за 24ч < threshold → fail
  if [ "$THRESHOLD" -gt 0 ] && [ "$U24" -lt "$THRESHOLD" ]; then
    warn "Active 24h ($U24) < threshold ($THRESHOLD) — каскад мало используется"
    EXIT_CODE=2
  fi

  # 5. Сравнение с прямым exit-узлом (тот же xray_api_host = тот же exit)
  EXIT_ID=$($PSQL -At -c "SELECT id FROM vpn_servers WHERE host = '$API_HOST' AND xray_api_host = '$API_HOST' LIMIT 1;")
  if [ -n "$EXIT_ID" ]; then
    EXIT_STATS=$($PSQL -At -F'|' -c "
      SELECT
        COUNT(DISTINCT vpn_user_id) FILTER (WHERE collected_at >= NOW() - INTERVAL '24 hours'),
        pg_size_pretty(COALESCE(SUM(uplink_bytes+downlink_bytes) FILTER (WHERE collected_at >= NOW() - INTERVAL '24 hours'), 0))
      FROM traffic_samples WHERE server_id = $EXIT_ID;
    ")
    IFS='|' read -r EXIT_U24 EXIT_BYTES <<< "$EXIT_STATS"
    printf "  🔄 Прямой exit [id=%s]: 24h=%s юзеров, %s\n" "$EXIT_ID" "$EXIT_U24" "$EXIT_BYTES"

    if [ "$EXIT_U24" -gt 0 ] && [ "$U24" -gt 0 ]; then
      RATIO=$(awk "BEGIN{printf \"%.1f\", ($U24*100/$EXIT_U24)}")
      printf "  📊 Cascade adoption: \033[1m%s%%\033[0m юзеров FI выбрали каскад\n" "$RATIO"
    fi
  fi

  hr
done

exit $EXIT_CODE
