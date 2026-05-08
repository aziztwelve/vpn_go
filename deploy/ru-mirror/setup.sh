#!/usr/bin/env bash
# Bootstrap RU-mirror VPS (Ubuntu 22.04 / 24.04).
# Запускать НА RU-VPS под root после того как ты:
#   1) уже сгенерировал ключи на обеих сторонах,
#   2) отредактировал /etc/wireguard/wg0.conf по wireguard.md,
#   3) залил в /opt/ru-mirror/ файлы Caddyfile, docker-compose.yml, .env.
#
# Скрипт идемпотентный — safe to re-run.

set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "Run as root: sudo $0" >&2
  exit 1
fi

echo "==> apt update + base packages"
apt-get update -qq
apt-get install -y -qq \
  ca-certificates curl gnupg lsb-release ufw \
  wireguard wireguard-tools

if ! command -v docker >/dev/null 2>&1; then
  echo "==> install docker (official repo)"
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
    | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
  chmod a+r /etc/apt/keyrings/docker.gpg
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
        https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" \
    > /etc/apt/sources.list.d/docker.list
  apt-get update -qq
  apt-get install -y -qq docker-ce docker-ce-cli containerd.io \
    docker-buildx-plugin docker-compose-plugin
fi

echo "==> firewall (ufw)"
ufw --force reset >/dev/null
ufw default deny incoming
ufw default allow outgoing
ufw allow 22/tcp comment 'ssh'
ufw allow 80/tcp  comment 'caddy http (LE)'
ufw allow 443/tcp comment 'caddy https'
ufw allow 443/udp comment 'caddy http3'
ufw --force enable

echo "==> WireGuard"
if [[ ! -f /etc/wireguard/wg0.conf ]]; then
  cat <<EOF >&2
ERROR: /etc/wireguard/wg0.conf not found.
Заполни конфиг по deploy/ru-mirror/wireguard.md (Шаг 3, RU-сторона)
и перезапусти setup.sh.
EOF
  exit 1
fi
chmod 600 /etc/wireguard/wg0.conf
systemctl enable --now wg-quick@wg0

echo "    waiting for tunnel..."
for i in {1..10}; do
  if ping -c 1 -W 1 10.13.13.1 >/dev/null 2>&1; then
    echo "    tunnel up (10.13.13.1 reachable)"
    break
  fi
  sleep 1
  if [[ $i == 10 ]]; then
    echo "ERROR: WG tunnel down — 10.13.13.1 не пингуется." >&2
    echo "Проверь wg show и FI-сторону." >&2
    exit 1
  fi
done

echo "==> sanity: gateway:8081 reachable through tunnel"
if ! curl -fsS -m 5 "http://10.13.13.1:8081/health" >/dev/null; then
  echo "ERROR: gateway:8081 не отвечает на 10.13.13.1." >&2
  echo "Проверь на FI-VPS:" >&2
  echo "  - docker compose ps gateway" >&2
  echo "  - ports должен включать '10.13.13.1:8081:8081'" >&2
  exit 1
fi
echo "    gateway healthy"

echo "==> Caddy (docker compose)"
cd /opt/ru-mirror
if [[ ! -f .env ]]; then
  cat <<EOF >&2
ERROR: /opt/ru-mirror/.env not found.
Скопируй .env.example в .env и заполни RU_DOMAIN + ACME_EMAIL.
EOF
  exit 1
fi
docker compose pull
docker compose up -d

echo "==> wait for Caddy + LE cert"
sleep 5
RU_DOMAIN=$(grep -E '^RU_DOMAIN=' .env | cut -d= -f2-)
echo "    Caddy logs:"
docker compose logs --tail 20 caddy

echo
echo "Done. Smoke-tests с любой машины (не с этого VPS, иначе локалхост):"
echo "  curl -sI https://${RU_DOMAIN}/health"
echo "  curl -sI https://${RU_DOMAIN}/api/v1/subscription/<token>"
