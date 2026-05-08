#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# stats.sh — on-demand сводка состояния VPN-системы.
#
# Выводит:
#   - Юзеры в системе (auth + vpn_users + сегодня/24ч)
#   - Подписки по статусам
#   - Активность сейчас (5min/1h/24h/7d) по двум источникам:
#       traffic_samples — реальный трафик через xray
#       subscription_fetches — клиенты тянули подписку
#   - Распределение по серверам с топ-юзерами по трафику
#
# Запуск: ./deploy/scripts/stats.sh [--json]
#
# Все запросы идут в локальный vpn-postgres через docker exec.
# -----------------------------------------------------------------------------
set -euo pipefail

JSON_MODE=false
if [[ "${1:-}" == "--json" ]]; then JSON_MODE=true; fi

PSQL="docker exec -i vpn-postgres psql -U vpn -d vpn"

if $JSON_MODE; then
  $PSQL -At -c "
    WITH users_stats AS (
      SELECT
        (SELECT COUNT(*) FROM users) AS total,
        (SELECT COUNT(*) FROM users WHERE created_at >= CURRENT_DATE) AS today,
        (SELECT COUNT(*) FROM users WHERE created_at >= NOW() - INTERVAL '24 hours') AS last_24h
    ),
    vpn_stats AS (
      SELECT
        (SELECT COUNT(*) FROM vpn_users) AS total,
        (SELECT COUNT(*) FROM vpn_users WHERE created_at >= CURRENT_DATE) AS today,
        (SELECT COUNT(*) FROM vpn_users WHERE created_at >= NOW() - INTERVAL '24 hours') AS last_24h
    ),
    activity AS (
      SELECT
        COUNT(DISTINCT vpn_user_id) FILTER (WHERE collected_at >= NOW() - INTERVAL '5 minutes') AS online_5min,
        COUNT(DISTINCT vpn_user_id) FILTER (WHERE collected_at >= NOW() - INTERVAL '1 hour') AS active_1h,
        COUNT(DISTINCT vpn_user_id) FILTER (WHERE collected_at >= NOW() - INTERVAL '24 hours') AS active_24h
      FROM traffic_samples
    )
    SELECT json_build_object(
      'users', json_build_object('total', us.total, 'today', us.today, 'last_24h', us.last_24h),
      'vpn_users', json_build_object('total', vs.total, 'today', vs.today, 'last_24h', vs.last_24h),
      'activity', json_build_object('online_5min', a.online_5min, 'active_1h', a.active_1h, 'active_24h', a.active_24h)
    )
    FROM users_stats us, vpn_stats vs, activity a;
  "
  exit 0
fi

bold()  { printf "\033[1m%s\033[0m\n" "$*"; }
hr()    { printf "\033[2m──────────────────────────────────────────────────────────────────\033[0m\n"; }

bold "📊 VPN STATS — $(date '+%Y-%m-%d %H:%M:%S %Z')"
hr

bold "👥 Юзеры"
$PSQL -c "
SELECT
  (SELECT COUNT(*) FROM users) AS users_total,
  (SELECT COUNT(*) FROM users WHERE created_at >= CURRENT_DATE) AS users_today,
  (SELECT COUNT(*) FROM users WHERE created_at >= NOW() - INTERVAL '24 hours') AS users_24h,
  (SELECT COUNT(*) FROM vpn_users) AS vpn_users_total,
  (SELECT COUNT(*) FROM vpn_users WHERE created_at >= CURRENT_DATE) AS vpn_users_today;
"

bold "💳 Подписки"
$PSQL -c "
SELECT
  status,
  COUNT(*),
  COUNT(*) FILTER (WHERE expires_at > NOW())  AS valid,
  COUNT(*) FILTER (WHERE expires_at <= NOW()) AS expired_at_now
FROM subscriptions
GROUP BY status
ORDER BY COUNT(*) DESC;
"

bold "⚡ Активность сейчас (по реальному трафику в xray)"
$PSQL -c "
SELECT
  COUNT(DISTINCT vpn_user_id) FILTER (WHERE collected_at >= NOW() - INTERVAL '5 minutes')  AS online_5min,
  COUNT(DISTINCT vpn_user_id) FILTER (WHERE collected_at >= NOW() - INTERVAL '1 hour')      AS active_1h,
  COUNT(DISTINCT vpn_user_id) FILTER (WHERE collected_at >= NOW() - INTERVAL '24 hours')    AS active_24h,
  COUNT(DISTINCT vpn_user_id) FILTER (WHERE collected_at >= NOW() - INTERVAL '7 days')      AS active_7d
FROM traffic_samples;
"

bold "📡 Распределение по серверам (24ч)"
$PSQL -c "
SELECT
  s.id, s.name, s.host,
  COUNT(DISTINCT ts.vpn_user_id) FILTER (WHERE ts.collected_at >= NOW() - INTERVAL '5 min')   AS u5m,
  COUNT(DISTINCT ts.vpn_user_id) FILTER (WHERE ts.collected_at >= NOW() - INTERVAL '1 hour')  AS u1h,
  COUNT(DISTINCT ts.vpn_user_id) FILTER (WHERE ts.collected_at >= NOW() - INTERVAL '24 hours') AS u24h,
  pg_size_pretty(COALESCE(SUM(ts.uplink_bytes + ts.downlink_bytes) FILTER (WHERE ts.collected_at >= NOW() - INTERVAL '24 hours'), 0)) AS traffic_24h
FROM vpn_servers s
LEFT JOIN traffic_samples ts ON ts.server_id = s.id
WHERE s.is_active = TRUE
GROUP BY s.id, s.name, s.host
ORDER BY s.id;
"

bold "🏆 Топ-5 юзеров по трафику (24ч)"
$PSQL -c "
SELECT
  vu.id, vu.email,
  COUNT(DISTINCT ts.server_id) AS srv_count,
  pg_size_pretty(SUM(ts.uplink_bytes + ts.downlink_bytes)) AS traffic,
  MAX(ts.collected_at) AT TIME ZONE 'UTC' AS last_seen_utc
FROM traffic_samples ts
JOIN vpn_users vu ON vu.id = ts.vpn_user_id
WHERE ts.collected_at >= NOW() - INTERVAL '24 hours'
GROUP BY vu.id, vu.email
ORDER BY SUM(ts.uplink_bytes + ts.downlink_bytes) DESC
LIMIT 5;
"

bold "📱 Топ-устройств (24ч, по subscription_fetches)"
$PSQL -c "
SELECT device_identifier, COUNT(*) AS fetches, COUNT(DISTINCT vpn_user_id) AS uniq_users
FROM subscription_fetches
WHERE last_seen >= NOW() - INTERVAL '24 hours'
GROUP BY device_identifier
ORDER BY fetches DESC
LIMIT 10;
"

hr
echo "Tip: ./deploy/scripts/stats.sh --json для машинного формата"
