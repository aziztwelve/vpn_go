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
- `format` (query, optional) - формат ответа: `json` или по умолчанию base64

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

Subscription предоставляет **3 конфигурации** с одинаковыми параметрами подключения, но разными названиями:

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
