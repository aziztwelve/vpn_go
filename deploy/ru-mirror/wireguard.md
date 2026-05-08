# WireGuard `RU-VPS ↔ FI-VPS`

Приватный туннель для проксирования подписочного эндпоинта с RU-зеркала на
основной gateway. Внешний интернет НЕ нужен — gateway:8081 на FI открывается
только на WG-IP.

## Топология

```
                  ┌──────────────────┐
                  │ RU-VPS (Timeweb) │
                  │   Caddy :443     │
                  │   wg0: 10.13.13.2│
                  └────────┬─────────┘
                           │ WG (UDP/51820)
                           │ ChaCha20+Poly1305
                  ┌────────▼─────────┐
                  │ FI-VPS (origin)  │
                  │   wg0: 10.13.13.1│
                  │   gateway :8081  │
                  │     bind 10.13.13.1
                  └──────────────────┘
```

- Сеть: `10.13.13.0/24`
- FI = `10.13.13.1` (server)
- RU = `10.13.13.2` (client/peer)
- UDP-порт: `51820` (классика wg)

## Шаг 1. Сгенерировать ключи

На обеих машинах:

```bash
sudo apt-get update && sudo apt-get install -y wireguard
mkdir -p /etc/wireguard && cd /etc/wireguard
umask 077
wg genkey | tee privatekey | wg pubkey > publickey
cat publickey
```

Сохрани публичные ключи обеих сторон — пригодятся.

## Шаг 2. FI-VPS (server)

`/etc/wireguard/wg0.conf`:

```ini
[Interface]
Address = 10.13.13.1/24
ListenPort = 51820
PrivateKey = <FI_PRIVATE_KEY>

# Включаем форвардинг при старте — на случай если будем гонять туннель
# дальше во внутреннюю сеть. Для нашего минимального сценария
# (только до gateway:8081) можно опустить.
PostUp   = iptables -A FORWARD -i wg0 -j ACCEPT
PostUp   = iptables -t nat -A POSTROUTING -o eth0 -j MASQUERADE
PostDown = iptables -D FORWARD -i wg0 -j ACCEPT
PostDown = iptables -t nat -D POSTROUTING -o eth0 -j MASQUERADE

[Peer]
# RU-mirror VPS
PublicKey = <RU_PUBLIC_KEY>
AllowedIPs = 10.13.13.2/32
```

Открыть порт WG в UFW:

```bash
sudo ufw allow 51820/udp comment 'WireGuard RU-mirror'
sudo systemctl enable --now wg-quick@wg0
sudo wg show          # peer должен появиться после поднятия RU-стороны
```

### Привязать gateway к WG IP

В `deploy/compose/docker-compose.yml`, в сервисе `gateway`, добавить
`ports`:

```yaml
gateway:
  ports:
    - "10.13.13.1:8081:8081"   # RU-mirror за WG-туннелем
```

После этого:

```bash
docker compose up -d gateway
ss -lnt | grep 8081           # должен быть LISTEN на 10.13.13.1:8081
```

> ⚠️ Не биндим к `0.0.0.0:8081` — иначе gateway смотрит в публичный
> интернет и обходит Caddy. Только WG-IP.

## Шаг 3. RU-VPS (client)

`/etc/wireguard/wg0.conf`:

```ini
[Interface]
Address = 10.13.13.2/24
PrivateKey = <RU_PRIVATE_KEY>

[Peer]
# FI-VPS
PublicKey = <FI_PUBLIC_KEY>
Endpoint = 178.104.217.201:51820
AllowedIPs = 10.13.13.1/32
PersistentKeepalive = 25
```

```bash
sudo systemctl enable --now wg-quick@wg0
ping -c 3 10.13.13.1          # должно отвечать
curl -s http://10.13.13.1:8081/health    # gateway → 200
```

## Проверка end-to-end

С RU-VPS:

```bash
# Берём любой реальный subscription_token из БД (vpn_users.subscription_token).
TOKEN=...
curl -sI -H "Host: s.osmonai.com" \
     -H "X-Forwarded-Host: s.osmonai.com" \
     -H "X-Forwarded-Proto: https" \
     "http://10.13.13.1:8081/api/v1/subscription/${TOKEN}" | head -5
```

Должен прийти `HTTP/1.1 200 OK` с заголовком `Profile-Web-Page-URL: https://s.osmonai.com`.

## Откат

Если что-то пошло не так:

```bash
# На FI:
sudo systemctl stop wg-quick@wg0
sudo ufw delete allow 51820/udp
# Удалить порт-маппинг 10.13.13.1:8081:8081 из docker-compose.yml
docker compose up -d gateway

# На RU:
sudo systemctl stop wg-quick@wg0
docker compose -f /opt/ru-mirror/docker-compose.yml down
```

Никаких persistent-изменений в gateway/коде нет — он остаётся доступным
через Caddy на FI как обычно.
