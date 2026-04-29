#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# deploy-xray-new.sh — развернуть новый Xray-VPS и подготовить INSERT
# для vpn_servers + команду ResyncServer.
#
# Что делает:
#   1. Генерирует свежие Reality x25519 keys + short_id
#   2. Создаёт config.json для Xray с этими ключами
#   3. Печатает SQL INSERT для vpn_servers и команду grpcurl ResyncServer
#
# НЕ делает (делаешь сам):
#   - ssh на VPS и `docker run` Xray (инструкция в конце)
#   - INSERT SQL в прод БД
#   - Резолвит DNS/домен
#
# Usage:
#   ./deploy/scripts/deploy-xray-new.sh \
#       --name "Germany-01" \
#       --location "Frankfurt" \
#       --country DE \
#       --host "de01.maydavpn.com" \
#       --port 8443 \
#       --max-conn 2000
#
# Почему по умолчанию :8443, а не :443:
#   В РФ ТСПУ/РКН активно режут TLS-handshake (дропают Server Hello)
#   на :443 для известных VPN-ASN — клиент видит «SSL connection
#   timeout», хотя TCP проходит. На нестандартных портах (:8443, :2053
#   и т.п.) такого правила нет. Подтверждено на 29.04.2026 на FI и NL
#   через mtr 0% loss + curl --resolve с RF-VPS. См. также memory от
#   29.04.2026 раздел «:443 dpi block».
# -----------------------------------------------------------------------------
set -euo pipefail

NAME=""
LOCATION=""
COUNTRY=""
HOST=""
PORT=8443
MAX_CONN=1000
DEST="apple.com:443"
SNI="apple.com"
INBOUND_TAG="vless-reality-in"
DESCRIPTION=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --name)        NAME="$2"; shift 2 ;;
    --location)    LOCATION="$2"; shift 2 ;;
    --country)     COUNTRY="$2"; shift 2 ;;
    --host)        HOST="$2"; shift 2 ;;
    --port)        PORT="$2"; shift 2 ;;
    --max-conn)    MAX_CONN="$2"; shift 2 ;;
    --dest)        DEST="$2"; shift 2 ;;
    --sni)         SNI="$2"; shift 2 ;;
    --inbound-tag) INBOUND_TAG="$2"; shift 2 ;;
    --description) DESCRIPTION="$2"; shift 2 ;;
    *) echo "unknown: $1" >&2; exit 1 ;;
  esac
done

for v in NAME LOCATION COUNTRY HOST; do
  if [ -z "${!v}" ]; then
    echo "❌ --${v,,} is required" >&2
    exit 1
  fi
done

echo "==> Генерация Reality x25519 keypair..."
KEYS=$(docker run --rm ghcr.io/xtls/xray-core:latest x25519 2>/dev/null)
PRIVATE_KEY=$(echo "$KEYS" | awk -F': ' '/^PrivateKey/{print $2}')
PUBLIC_KEY=$(echo  "$KEYS" | awk -F': ' '/^Password/{print $2}')
SHORT_ID=$(openssl rand -hex 8)

echo "✅ Keys generated:"
echo "   PrivateKey: $PRIVATE_KEY"
echo "   PublicKey:  $PUBLIC_KEY"
echo "   ShortID:    $SHORT_ID"
echo

# Сохраняем в артефакт на локальной машине.
mkdir -p deploy/compose/xray-new
OUT_DIR="deploy/compose/xray-new/$(echo "$NAME" | tr -cs 'A-Za-z0-9' '_')"
mkdir -p "$OUT_DIR"
cat > "$OUT_DIR/config.json" <<EOF
{
  "log": {"loglevel": "info"},
  "api": {"tag": "api", "services": ["HandlerService","StatsService","LoggerService"]},
  "stats": {},
  "policy": {"levels": {"0": {"statsUserUplink": true,"statsUserDownlink": true}}, "system": {"statsInboundUplink": true,"statsInboundDownlink": true}},
  "routing": {"rules": [{"type":"field","inboundTag":["api"],"outboundTag":"api"}]},
  "inbounds": [
    {"tag":"api","listen":"0.0.0.0","port":10085,"protocol":"dokodemo-door","settings":{"address":"127.0.0.1"}},
    {
      "tag":"${INBOUND_TAG}",
      "listen":"0.0.0.0","port":${PORT},"protocol":"vless",
      "settings":{"clients":[],"decryption":"none"},
      "streamSettings":{
        "network":"tcp","security":"reality",
        "realitySettings":{
          "show":false,"dest":"${DEST}","xver":0,
          "serverNames":["${SNI}"],
          "privateKey":"${PRIVATE_KEY}","shortIds":["${SHORT_ID}"]
        }
      },
      "sniffing":{"enabled":true,"destOverride":["http","tls","quic"]}
    }
  ],
  "outbounds":[{"protocol":"freedom","tag":"direct"},{"protocol":"blackhole","tag":"blocked"}]
}
EOF

echo "✅ Xray config сохранён в $OUT_DIR/config.json"
echo

# ----- SQL insert -----
DESC_SQL=$(printf '%s' "${DESCRIPTION:-$NAME ($LOCATION)}" | sed "s/'/''/g")
cat <<EOF

==> Шаг 1. Запусти Xray на $HOST:
    scp -r $OUT_DIR root@$HOST:/opt/xray
    ssh root@$HOST 'docker run -d --name xray \\
        --restart unless-stopped \\
        -v /opt/xray/config.json:/etc/xray/config.json:ro \\
        -p ${PORT}:${PORT} -p 10085:10085 \\
        ghcr.io/xtls/xray-core:latest -c /etc/xray/config.json'

==> Шаг 2. INSERT в БД (на backend VPS):
    docker exec -i vpn-postgres psql -U vpn -d vpn <<'SQL'
    INSERT INTO vpn_servers (
        name, location, country_code, host, port,
        public_key, private_key, short_id, dest, server_names,
        xray_api_host, xray_api_port, inbound_tag, is_active,
        server_max_connections, description
    ) VALUES (
        '${NAME}','${LOCATION}','${COUNTRY}','${HOST}',${PORT},
        '${PUBLIC_KEY}','${PRIVATE_KEY}','${SHORT_ID}','${DEST}','${SNI}',
        '${HOST}',10085,'${INBOUND_TAG}',true,
        ${MAX_CONN},'${DESC_SQL}'
    ) RETURNING id;
    SQL

==> Шаг 3. Re-seed существующих юзеров в новый inbound:
    # Предположим INSERT вернул id=2
    docker run --rm --network vpn-stack_vpn fullstorydev/grpcurl:latest \\
      -plaintext -d '{"server_id": 2}' \\
      vpn-core:50062 vpn.v1.VPNService/ResyncServer
    # Ответ: {"usersTotal":N, "usersAdded":N, ...}

==> Готово! Новый сервер работает.
EOF
