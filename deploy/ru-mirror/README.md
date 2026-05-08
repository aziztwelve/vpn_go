# RU-mirror подписочного эндпоинта

Опция А из обсуждения 2026-05-08 (см. `docs/tasks/16-rkn-resilience.md` Stage 2).
Поднимаем тонкое RU-зеркало для `GET /api/v1/subscription/{token}`, чтобы
HAPP/Hiddify/V2RayNG-клиенты в РФ не зависели от `cdn.osmonai.com` за
Cloudflare. Mini App, API и оплата остаются на FI-стенде как сейчас.

```
              ┌────────────────────────┐                ┌───────────────────┐
client (RU) ──┤ s.osmonai.com (Caddy)  │── WG tunnel ──┤ FI gateway:8081    │
              │  RU-VPS, Timeweb       │  10.13.13.0/24│  178.104.217.201   │
              └────────────────────────┘                └───────────────────┘
```

## Файлы

| Файл | Назначение |
|---|---|
| `Caddyfile` | RU-Caddy: TLS на `s.osmonai.com`, прокси `/api/v1/subscription/*` через WG |
| `docker-compose.yml` | Один сервис — Caddy 2 |
| `.env.example` | Переменные (RU_DOMAIN, ACME_EMAIL, GATEWAY_UPSTREAM) |
| `wireguard.md` | Гайд по настройке WG: server (FI) + client (RU) |
| `setup.sh` | Bootstrap скрипт для RU-VPS — apt + ufw + wg + compose up |

## Чек-лист деплоя

### A. Подготовка

- [ ] Купить VPS на Timeweb: 1 vCPU / 1 GB RAM / Ubuntu 22.04+ (≈300 ₽/мес). Москва/СПб.
- [ ] Получить root-доступ, прокинуть свой SSH-ключ.
- [ ] Узнать публичный IP RU-VPS — пригодится для CF и WG.

### B. DNS (Cloudflare)

- [ ] В CF-зоне `osmonai.com` добавить A-запись:
  - **Name:** `s`
  - **Content:** `<IP RU-VPS>`
  - **Proxy:** ☁️ **DNS only** (НЕ оранжевое облако!) — иначе CF перехватит трафик и весь смысл RU-зеркала пропадёт.
  - **TTL:** Auto (300 сек).
- [ ] Дождаться пропагации (`dig +short s.osmonai.com @1.1.1.1` должен вернуть IP RU-VPS).

### C. WireGuard

См. `wireguard.md`. Кратко:

- [ ] На обеих сторонах: `apt install wireguard`, `wg genkey | tee priv | wg pubkey > pub`.
- [ ] FI: `/etc/wireguard/wg0.conf` (server, 10.13.13.1), peer = RU pubkey.
- [ ] RU: `/etc/wireguard/wg0.conf` (client, 10.13.13.2), endpoint = `<FI-IP>:51820`.
- [ ] FI: `ufw allow 51820/udp`, `systemctl enable --now wg-quick@wg0`.
- [ ] RU: `systemctl enable --now wg-quick@wg0`.
- [ ] Пинг: `ping 10.13.13.1` с RU должен отвечать.

### D. Привязать FI gateway к WG-IP

В `vpn_go/deploy/compose/docker-compose.yml`, сервис `gateway`, добавить:

```yaml
gateway:
  ports:
    - "10.13.13.1:8081:8081"   # RU-mirror
```

Затем на FI-VPS:

```bash
docker compose up -d gateway
ss -lnt | grep 8081     # должен быть LISTEN на 10.13.13.1:8081
```

### E. Запустить Caddy на RU-VPS

```bash
# На RU-VPS:
mkdir -p /opt/ru-mirror && cd /opt/ru-mirror
# залей сюда Caddyfile, docker-compose.yml, .env.example, setup.sh
cp .env.example .env
$EDITOR .env             # подставить RU_DOMAIN, ACME_EMAIL
sudo bash setup.sh
```

`setup.sh` поставит docker, поднимет UFW, проверит туннель, запустит Caddy.
LE-серт получится автоматически по HTTP-01.

### F. Smoke-тест

С любой машины (не с самого RU-VPS):

```bash
curl -sI https://s.osmonai.com/health
# HTTP/2 200, x-powered-by-caddy

# Возьми реальный subscription_token из БД:
docker exec vpn-postgres psql -U vpn -d vpn -c \
  "SELECT subscription_token FROM vpn_users LIMIT 1;"

TOKEN=...
curl -sI https://s.osmonai.com/api/v1/subscription/$TOKEN
# HTTP/2 200, profile-update-interval, profile-web-page-url, ...
```

`Profile-Web-Page-URL` должен быть `https://s.osmonai.com` (т.е. RU-зеркало
само себя возвращает) — это работает за счёт правки `subscription_config.go:143`.

### G. Переключить юзеров на RU-домен

В `vpn_go/deploy/env/.env` (FI-VPS), gateway env:

```bash
PUBLIC_BASE_URL=https://s.osmonai.com
```

После `docker compose up -d gateway` все новые ответы `GET /vpn/subscription-token`
вернут `subscription_url=https://s.osmonai.com/api/v1/subscription/<token>`. Старые
клиенты с `cdn.osmonai.com`-ссылкой продолжают работать пока CF-домен жив.

## Что мониторить

- **На RU-VPS:** `docker compose logs -f caddy` — ищем 5xx и `tls.handshake` ошибки.
- **На FI-VPS:** `docker logs -f vpn-gateway 2>&1 | grep s.osmonai` — запросы должны идти с `Host: s.osmonai.com`.
- **WG-туннель:** `wg show` обе стороны — handshake свежее минут 5; если устаревает → проверь `PersistentKeepalive=25` на RU.
- **Пропускная способность:** `vnstat -i wg0` — оценка трафика. Подписочный эндпоинт лёгкий (~5 KB ответ), даже на пиковых днях должно быть единицы MB/час.

## Откат

1. На FI: убрать `PUBLIC_BASE_URL=https://s.osmonai.com` (вернуть на `cdn.osmonai.com`), `docker compose up -d gateway`.
2. На FI: убрать `10.13.13.1:8081:8081` из ports у gateway, `docker compose up -d gateway`.
3. На FI: `systemctl stop wg-quick@wg0`, `ufw delete allow 51820/udp`.
4. На RU-VPS: `docker compose down`.
5. CF: удалить A-запись `s.osmonai.com`.

Все изменения локальны (env + ports), не трогают БД и схемы — откат за 2 минуты.

## Расширения (потом, не сейчас)

- **Кэш в Caddy 60-120 сек** уже включён через `Cache-Control` заголовок (HAPP уважает).
- **Failover на API/Mini App** — поднять то же самое на vpn_next и api (`tasks/16-rkn-resilience.md` Stage 2 в полном объёме).
- **HA mirror** с локальным Postgres-replica — `tasks/08-ha-backend-mirror.md`. Делать когда будет 100+ платящих юзеров.
