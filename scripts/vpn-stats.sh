#!/bin/bash
# VPN Statistics Script
# Usage: ./scripts/vpn-stats.sh [today|week|active|payments|all]

set -e

DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5433}"
DB_USER="${DB_USER:-vpn}"
DB_NAME="${DB_NAME:-vpn}"
DB_PASSWORD="${DB_PASSWORD:-change_me_strong_password}"

export PGPASSWORD="$DB_PASSWORD"

PSQL="psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -t -A"

MODE="${1:-all}"

echo "=== VPN Statistics ==="
echo "Generated: $(date -u '+%Y-%m-%d %H:%M:%S UTC')"
echo ""

# Total users
if [[ "$MODE" == "all" || "$MODE" == "total" ]]; then
    echo "--- Total Users ---"
    TOTAL=$($PSQL -c "SELECT COUNT(*) FROM users;")
    VPN_USERS=$($PSQL -c "SELECT COUNT(*) FROM vpn_users;")
    echo "Registered: $TOTAL"
    echo "VPN configs created: $VPN_USERS ($(awk "BEGIN {printf \"%.1f\", ($VPN_USERS/$TOTAL)*100}")%)"
    echo ""
fi

# New users today
if [[ "$MODE" == "all" || "$MODE" == "today" ]]; then
    echo "--- New Users Today ---"
    NEW_TODAY=$($PSQL -c "SELECT COUNT(*) FROM users WHERE DATE(created_at) = CURRENT_DATE;")
    echo "New registrations: $NEW_TODAY"
    
    if [[ $NEW_TODAY -gt 0 ]]; then
        echo ""
        echo "List:"
        $PSQL -c "SELECT 
            u.telegram_id,
            COALESCE(u.username, '-') as username,
            COALESCE(u.first_name, '-') as name,
            TO_CHAR(u.created_at, 'HH24:MI') as time,
            CASE WHEN vu.id IS NOT NULL THEN 'VPN✓' ELSE '-' END as vpn_status
        FROM users u
        LEFT JOIN vpn_users vu ON u.id = vu.user_id
        WHERE DATE(u.created_at) = CURRENT_DATE
        ORDER BY u.created_at DESC;" | column -t -s '|'
    fi
    echo ""
fi

# New users this week
if [[ "$MODE" == "all" || "$MODE" == "week" ]]; then
    echo "--- New Users (Last 7 Days) ---"
    $PSQL -c "SELECT 
        DATE(created_at) as date,
        COUNT(*) as new_users
    FROM users
    WHERE created_at >= NOW() - INTERVAL '7 days'
    GROUP BY DATE(created_at)
    ORDER BY date DESC;" | column -t -s '|'
    echo ""
fi

# Active users
if [[ "$MODE" == "all" || "$MODE" == "active" ]]; then
    echo "--- Active Users ---"
    ACTIVE_NOW=$($PSQL -c "SELECT COUNT(DISTINCT vpn_user_id) FROM traffic_samples WHERE collected_at >= NOW() - INTERVAL '5 minutes';")
    ACTIVE_1H=$($PSQL -c "SELECT COUNT(DISTINCT vpn_user_id) FROM traffic_samples WHERE collected_at >= NOW() - INTERVAL '1 hour';")
    ACTIVE_24H=$($PSQL -c "SELECT COUNT(DISTINCT vpn_user_id) FROM traffic_samples WHERE collected_at >= NOW() - INTERVAL '24 hours';")
    ACTIVE_TODAY=$($PSQL -c "SELECT COUNT(DISTINCT vpn_user_id) FROM traffic_samples WHERE DATE(collected_at) = CURRENT_DATE;")
    
    echo "Now (5 min): $ACTIVE_NOW"
    echo "Last hour: $ACTIVE_1H"
    echo "Last 24h: $ACTIVE_24H"
    echo "Today: $ACTIVE_TODAY"
    echo ""
fi

# Payments today
if [[ "$MODE" == "all" || "$MODE" == "payments" ]]; then
    echo "--- Payments Today ---"
    PAYMENTS_TODAY=$($PSQL -c "SELECT COUNT(*) FROM payments WHERE DATE(created_at) = CURRENT_DATE AND status = 'paid';")
    REVENUE_TODAY=$($PSQL -c "SELECT COALESCE(SUM(amount_rub), 0) FROM payments WHERE DATE(created_at) = CURRENT_DATE AND status = 'paid';")
    
    echo "Successful payments: $PAYMENTS_TODAY"
    echo "Revenue: ${REVENUE_TODAY} RUB"
    
    if [[ $(echo "$PAYMENTS_TODAY > 0" | bc) -eq 1 ]]; then
        echo ""
        echo "Details:"
        $PSQL -c "SELECT 
            u.telegram_id,
            COALESCE(u.username, '-') as username,
            p.amount_rub || ' ' || p.currency as amount,
            TO_CHAR(p.paid_at, 'HH24:MI') as paid_time
        FROM payments p
        JOIN users u ON p.user_id = u.id
        WHERE DATE(p.created_at) = CURRENT_DATE AND p.status = 'paid'
        ORDER BY p.paid_at DESC;" | column -t -s '|'
    fi
    echo ""
fi

# Top users by traffic today
if [[ "$MODE" == "all" || "$MODE" == "traffic" ]]; then
    echo "--- Top 10 Users by Traffic Today ---"
    $PSQL -c "SELECT 
        u.telegram_id,
        COALESCE(u.username, '-') as username,
        ROUND(SUM(t.uplink_bytes + t.downlink_bytes) / 1024.0 / 1024.0, 1) || ' MB' as traffic
    FROM users u
    JOIN vpn_users vu ON u.id = vu.user_id
    JOIN traffic_samples t ON vu.id = t.vpn_user_id
    WHERE DATE(t.collected_at) = CURRENT_DATE
    GROUP BY u.id, u.telegram_id, u.username
    ORDER BY SUM(t.uplink_bytes + t.downlink_bytes) DESC
    LIMIT 10;" | column -t -s '|'
    echo ""
fi

echo "=== End of Statistics ==="
