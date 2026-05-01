# Subscription Endpoint

## Обзор

Subscription endpoint предоставляет VPN конфигурации для клиентов (Happ, V2RayNG, Hiddify, etc.) в двух форматах:
- **Base64** (по умолчанию) - список VLESS ссылок
- **JSON** (с параметром `?format=json`) - полная конфигурация Xray

## Endpoint

```
GET /api/v1/subscription/{token}
```

### Параметры

- `token` (path) - токен пользователя для идентификации
- `format` (query, optional) - формат ответа: `json` или `base64`. Если не задан — выбирается по `User-Agent`:
  - HAPP (`User-Agent: Happ/...`) → **JSON** (нужен для `🌐 АВТО ВЫБОР`)
  - Все остальные клиенты → **base64** (универсальный, работает в V2RayNG/Hiddify/Streisand/v2rayN)

### Headers

```
Profile-Update-Interval: 1
Subscription-Userinfo: upload=0; download=0; total=10737418240; expire=0
```

## Форматы ответа

### 1. Base64 (по умолчанию)

**URL:** `https://cdn.osmonai.com/api/v1/subscription/test`

**Content-Type:** `text/plain; charset=utf-8`

**Формат:** Base64 закодированный список VLESS ссылок (по одной на строку)

**Пример декодированного содержимого:**
```
vless://550e8400-e29b-41d4-a716-446655440000@178.104.217.201:8443?flow=xtls-rprx-vision&fp=chrome&headerType=&host=&path=&pbk=Npb1GRjWa5dEHU0aTPyxQxN4YSnjNSiniwt1IBNOUn0&security=reality&sid=e01417022de29ba0&sni=github.com&type=tcp#🚀 Обход блокировок
vless://550e8400-e29b-41d4-a716-446655440000@178.104.217.201:8443?flow=xtls-rprx-vision&fp=chrome&headerType=&host=&path=&pbk=Npb1GRjWa5dEHU0aTPyxQxN4YSnjNSiniwt1IBNOUn0&security=reality&sid=e01417022de29ba0&sni=github.com&type=tcp#🔒 Весь трафик
vless://550e8400-e29b-41d4-a716-446655440000@178.104.217.201:8443?flow=xtls-rprx-vision&fp=chrome&headerType=&host=&path=&pbk=Npb1GRjWa5dEHU0aTPyxQxN4YSnjNSiniwt1IBNOUn0&security=reality&sid=e01417022de29ba0&sni=github.com&type=tcp#🎬 YouTube без рекламы
```

**Использование:**
- Happ: `happ://add/https://cdn.osmonai.com/api/v1/subscription/test`
- V2RayNG: Добавить подписку → вставить URL
- Другие клиенты: импорт по URL

### 2. JSON

**URL:** `https://cdn.osmonai.com/api/v1/subscription/test?format=json`

**Content-Type:** `application/json; charset=utf-8`

**Формат:** Полная конфигурация Xray с DNS, routing, inbounds, outbounds

**Структура:**
```json
{
  "dns": {
    "hosts": {
      "cloudflare-dns.com": "1.1.1.1",
      "dns.google": "8.8.8.8"
    },
    "queryStrategy": "UseIPv4",
    "servers": [...]
  },
  "inbounds": [
    {
      "listen": "127.0.0.1",
      "port": 10808,
      "protocol": "socks",
      ...
    },
    {
      "listen": "127.0.0.1",
      "port": 10809,
      "protocol": "http",
      ...
    }
  ],
  "log": {
    "loglevel": "warning"
  },
  "outbounds": [
    {
      "protocol": "vless",
      "settings": {
        "vnext": [{
          "address": "178.104.217.201",
          "port": 8443,
          "users": [{
            "id": "550e8400-e29b-41d4-a716-446655440000",
            "encryption": "none",
            "flow": "xtls-rprx-vision"
          }]
        }]
      },
      "streamSettings": {
        "network": "tcp",
        "security": "reality",
        "realitySettings": {
          "fingerprint": "chrome",
          "publicKey": "Npb1GRjWa5dEHU0aTPyxQxN4YSnjNSiniwt1IBNOUn0",
          "serverName": "github.com",
          "shortId": "e01417022de29ba0"
        }
      },
      "tag": "proxy"
    },
    {
      "protocol": "freedom",
      "tag": "direct"
    },
    {
      "protocol": "blackhole",
      "tag": "block"
    }
  ],
  "routing": {
    "domainStrategy": "IPIfNonMatch",
    "rules": [...]
  }
}
```

**Использование:**
- Импорт в продвинутые клиенты с поддержкой JSON конфигураций
- Ручная настройка Xray/V2Ray

## Конфигурации

В **JSON-формате** (HAPP) список — `N серверов × 1 профиль` + опциональный `🌐 АВТО ВЫБОР` в конце:

```
⚡ Обычный VPN · 🇫🇮 Finland
⚡ Обычный VPN · 🇩🇪 Germany
⚡ Обычный VPN · 🇳🇱 Netherlands-01
🌐 АВТО ВЫБОР                     ← если серверов ≥2
```

В **base64-формате** (V2RayNG/Hiddify) список — 3 «режима» на дефолтную страну + по одной ссылке на каждый сервер:

```
⚡ Обычный VPN · 🇩🇪 Germany       ← 3 режима на defaultCountry
🚀 Обход блокировок · 🇩🇪 Germany     (один и тот же outbound,
🎬 YouTube без рекламы · 🇩🇪 Germany   разные routing-стратегии)
🇫🇮 Finland                        ← дальше — выбор конкретного
🇩🇪 Germany                          сервера/географии
🇳🇱 Netherlands-01
```

Subscription предоставляет **3 routing-конфигурации** с одинаковыми параметрами подключения, но разной обработкой трафика:

### 1. 🚀 Обход блокировок (по умолчанию)
- Только заблокированные сайты через VPN
- Остальной трафик напрямую
- Оптимально для России

**Routing rules:**
- Локальные IP → direct
- Реклама → block
- Facebook, Instagram, Twitter, YouTube, Telegram, Discord, LinkedIn, Medium, Reddit, Google, GitHub → proxy
- Всё остальное → direct

### 2. 🔒 Весь трафик
- Весь интернет-трафик через VPN
- Максимальная приватность
- Может быть медленнее

**Routing rules:**
- Локальные IP → direct
- Реклама → block
- Всё остальное → proxy

### 3. 🎬 YouTube без рекламы
- YouTube через VPN
- AdGuard DNS для блокировки рекламы
- Остальной трафик напрямую

**Routing rules:**
- Локальные IP → direct
- Реклама → block (через AdGuard DNS)
- YouTube, заблокированные соцсети → proxy
- Всё остальное → direct

### 4. 🌐 АВТО ВЫБОР (multi-server, JSON-формат)

**Эмитится только в JSON-формате и только при ≥2 активных серверах.**
В base64-выдаче не появляется — формат VLESS-URI не поддерживает balancer.

HAPP получает JSON автоматически (UA-sniff на бекенде, см. «Параметры» выше).
Другим клиентам, чтобы увидеть АВТО ВЫБОР, нужен явный `?format=json` —
но они и так его не понимают, так что смысла нет.

Один дополнительный конфиг в **конце** массива, который сам выбирает лучший VPS по
реальному RTT (HTTP-ping `gstatic.com/generate_204` через сам outbound, interval 1m)
и автопереключается при падении ноды (≤1 минуты).

**Зачем добавили в конец:** HAPP по умолчанию выбирает первый элемент массива как
активный сервер. Помещая АВТО ВЫБОР последним, мы не ломаем default selection у
уже подключённых клиентов — те, кто хочет, переключатся вручную.

**Routing-стратегия:**
- Локальные IP/RU GeoIP → direct (без VPN, как в Bypass-профиле)
- Apple/iCloud → direct (избегаем проблем с push-нотификациями)
- Всё остальное → balancer
- Если все ноды dead → blackhole (НЕ direct — для VPN-сервиса утечка реального
  IP при fallback'е недопустима)

**Структура (упрощённо):**
```json
{
  "remarks": "🌐 АВТО ВЫБОР",
  "outbounds": [
    {"tag": "proxy-1", "protocol": "vless", "settings": {...}, "streamSettings": {...}},
    {"tag": "proxy-2", "protocol": "vless", "settings": {...}, "streamSettings": {...}},
    {"tag": "proxy-N", "protocol": "vless", "settings": {...}, "streamSettings": {...}},
    {"tag": "direct", "protocol": "freedom"},
    {"tag": "block",  "protocol": "blackhole"}
  ],
  "routing": {
    "balancers": [{
      "tag": "Auto_Balancer",
      "selector": ["proxy"],
      "strategy": {"type": "leastLoad", "settings": {"expected": 2}},
      "fallbackTag": "block"
    }],
    "rules": [
      {"type":"field","ip":["geoip:private","geoip:ru"],"outboundTag":"direct"},
      {"type":"field","domain":["geosite:apple"],"outboundTag":"direct"},
      {"type":"field","network":"tcp,udp","balancerTag":"Auto_Balancer"}
    ]
  },
  "burstObservatory": {
    "subjectSelector": ["proxy"],
    "pingConfig": {
      "destination": "http://www.gstatic.com/generate_204",
      "interval": "1m",
      "timeout": "3s",
      "sampling": 1
    }
  }
}
```

**Ключевые моменты:**
- `subjectSelector: ["proxy"]` — prefix-match. Все outbound'ы с тегом
  `proxy-N` автоматически становятся кандидатами для observatory + balancer.
- `expected: 2` — leastLoad требует минимум 2 живых outbound'а, иначе
  идём в `fallbackTag: "block"`. С 1 кандидатом balancer бессмыслен.
- `burstObservatory` — top-level в конфиге (не внутри routing), это
  standalone-фича Xray (`xray-core/app/observatory/burst`).
- Health-check overhead: `~50 байт × N серверов / минута` (gstatic 204 ответ).

**Реализация:** [`services/gateway/internal/handler/subscription_auto.go`](../services/gateway/internal/handler/subscription_auto.go).
Тесты: `subscription_auto_test.go`.

См. также раздел [«Тестирование АВТО ВЫБОР»](#тестирование-авто-выбор-вручную) ниже.

## Технические детали

### VLESS параметры

```
protocol: vless
transport: tcp
security: reality
flow: xtls-rprx-vision
fingerprint: chrome
sni: github.com
```

### Reality параметры

```
publicKey: Npb1GRjWa5dEHU0aTPyxQxN4YSnjNSiniwt1IBNOUn0
shortId: e01417022de29ba0
serverName: github.com
```

### DNS

**По умолчанию:** Cloudflare DNS over HTTPS
```
https://cloudflare-dns.com/dns-query
```

**Для режима "YouTube без рекламы":** AdGuard DNS
```
https://dns.adguard.com/dns-query
```

## Интеграция с Mini App

На странице `/connect` в Mini App есть кнопка "📦 Добавить подписку в Happ":

```typescript
const subscriptionUrl = `https://cdn.osmonai.com/api/v1/subscription/test`;
openDeeplink(`happ://add/${subscriptionUrl}`);
```

## TODO

- [ ] Генерация уникального токена для каждого пользователя
- [ ] Валидация токена в handler
- [ ] Получение UUID пользователя из базы данных
- [ ] Получение списка серверов из базы (сейчас хардкод Germany)
- [ ] Поддержка нескольких серверов в одной подписке
- [ ] Автоматическое обновление конфигурации (Profile-Update-Interval)
- [ ] Статистика использования (upload/download/total в Subscription-Userinfo)

## Примеры использования

### cURL

```bash
# Base64 формат
curl https://cdn.osmonai.com/api/v1/subscription/test | base64 -d

# JSON формат
curl https://cdn.osmonai.com/api/v1/subscription/test?format=json | jq
```

### Happ (iOS/Android)

1. Открыть Happ
2. Нажать "+" → "Добавить подписку"
3. Вставить URL: `https://cdn.osmonai.com/api/v1/subscription/test`
4. Или использовать deeplink: `happ://add/https://cdn.osmonai.com/api/v1/subscription/test`

### V2RayNG (Android)

1. Открыть V2RayNG
2. Меню → Подписки → "+"
3. Вставить URL: `https://cdn.osmonai.com/api/v1/subscription/test`
4. Обновить подписку

## Тестирование «АВТО ВЫБОР» вручную

Что мы хотим проверить:
1. Какой сервер выбрал balancer (live RTT-замеры observatory).
2. Какой реальный exit IP у трафика (= какая VPS на самом деле обрабатывает запрос).
3. Распределение запросов между топ-кандидатами на длинной серии.

Тестировать удобнее **с отдельной машины, а не с самой VPN-ноды** — RTT до
"себя же" будет ~0, и balancer всегда будет выбирать локальный сервер. Подойдёт
любой сервер/ноут с docker.

### Шаг 1. Получить и пропатчить «АВТО ВЫБОР» из subscription

```bash
SUB_URL='https://cdn.osmonai.com/api/v1/subscription/<TOKEN>?format=json'

curl -sS "$SUB_URL" \
  | jq '
      # выдираем именно АВТО ВЫБОР
      map(select(.remarks | contains("АВТО"))) | .[0]
      # info-логи (видно taking detour [proxy-N] и observatory)
      | .log.loglevel = "info"
      # gRPC api для xray api bi
      | .api = {tag:"api", services:["RoutingService","StatsService"]}
      | .inbounds += [{
          tag:"api", listen:"127.0.0.1", port:10086,
          protocol:"dokodemo-door", settings:{address:"127.0.0.1"}
        }]
      | .routing.rules = ([{type:"field", inboundTag:["api"], outboundTag:"api"}] + (.routing.rules // []))
    ' > /tmp/auto.json

# Sanity-check: должны быть proxy-1..proxy-N + balancer
jq '{
  outbounds: [.outbounds[] | {tag, host: .settings.vnext[0].address}],
  balancer: .routing.balancers[0].tag,
  observatory: .burstObservatory.pingConfig.interval
}' /tmp/auto.json
```

### Шаг 2. Запустить xray в docker

```bash
docker run -d --name xray-test --network host \
  -v /tmp/auto.json:/etc/xray/config.json:ro \
  ghcr.io/xtls/xray-core:latest run -c /etc/xray/config.json

# Дать observatory сделать минимум один полный цикл (interval=1m)
sleep 65

docker ps --filter name=xray-test
docker logs xray-test 2>&1 | tail -20
```

### Шаг 3. Посмотреть, какой сервер выбран

#### Через `xray api bi` (состояние balancer'а + health)

```bash
docker exec xray-test xray api bi --server=127.0.0.1:10086 Auto_Balancer
# Вывод:
#   - Selecting Override:
#     1
#   - Selects:
#     1   proxy-N    ← победитель leastLoad
#     2   proxy-M
```

> **Note:** В Xray 26.x команды `xray api obs` НЕТ
> (`unknown command`). Состояние observatory смотрится через `bi` (selects
> сортируются по RTT) или через info-логи. `ObservatoryService` в
> `api.services` тоже не нужен — достаточно `RoutingService`.

#### Через exit IP

```bash
# Один запрос
EXIT=$(curl -sS --socks5-hostname 127.0.0.1:10808 https://api.ipify.org)
echo "exit IP = $EXIT"

# Сматчить с серверами в конфиге
jq -r '.outbounds[] | select(.tag|startswith("proxy")) | "\(.tag)\t\(.settings.vnext[0].address)"' /tmp/auto.json | column -t

# Распределение по 30 запросам
for i in $(seq 1 30); do
  curl -sS --socks5-hostname 127.0.0.1:10808 https://api.ipify.org
  echo
done | sort | uniq -c | sort -rn
```

#### Через info-логи

```bash
docker logs -f xray-test 2>&1 | grep -E 'observatory|taking platform initialized detour|dialing TCP'
# Каждое соединение через balancer логируется как:
#   app/dispatcher: taking platform initialized detour [proxy-N] for [tcp:...]
#   transport/internet/tcp: dialing TCP to tcp:<host>:8443
```

### Шаг 4. Cleanup

```bash
docker rm -f xray-test
rm /tmp/auto.json
```

### Гитчи

- **`--socks5-hostname`** (не `--socks5`) — иначе DNS резолвится локально и
  попадает не в balancer-rule, а в RU/private direct-rule.
- **`--network host`** в docker — `inbounds.listen: 127.0.0.1` иначе будет
  слушать loopback внутри контейнера, и host-side curl не достучится.
- **`network: "tcp,udp"`** в balancer-rule **обязателен**. Без него Xray
  валится `app/router: this rule has no effective fields`.
- **Минута ожидания при первом старте** — `burstObservatory.interval = 1m`.
  До первого тика `selects` пустые, balancer просто берёт первый outbound по
  списку. Xray делает дополнительный one-time health-check сразу при старте,
  так что первые замеры приходят за ~1-2 секунды, но полный цикл — 60s.
- **Тестировать с самой VPN-ноды бессмысленно** — RTT до loopback = ~15ms,
  до удалённой ноды = 30-150ms, balancer всегда выберет локальную.

## Troubleshooting

### Ошибка "token required"
- Убедитесь что в URL указан токен: `/api/v1/subscription/{token}`

### Happ не показывает серверы
- Проверьте что используется base64 формат (без `?format=json`)
- Убедитесь что VLESS ссылки корректны (проверьте через `base64 -d`)

### Не работает подключение
- Проверьте параметры Reality (publicKey, shortId, sni)
- Убедитесь что сервер доступен (178.104.217.201:8443)
- Проверьте UUID пользователя

## См. также

- [Gateway API](./services/gateway.md)
- [VPN Service](./specs/02-vpn-service.md)
- [Architecture](./ARCHITECTURE.md)
