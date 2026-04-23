# 04. Публичный домен + авто-TLS через Caddy

**Дата:** 2026-04-23  
**Статус:** 🟡 В работе — конфиги/compose/taskfile готовы, осталось проверить на VPS  
**Автор:** Devin + aziz  
**Родительский:** [02-mvp-c-implementation.md](./02-mvp-c-implementation.md) — **заменяет подпункты 9.2 и 9.7**

---

## 🎯 Цель

Предоставить публичный HTTPS-endpoint для Gateway (`/api/v1/...`) с автоматическим выпуском и продлением сертификата Let's Encrypt. Смена домена (пример: `api.osmonai.com` → `api.example.com`) — одна правка в `.env` + `docker compose up -d`, без ручного `certbot renew`, без правки nginx-конфига, без перезапуска всего стека.

**Домены:**
- `api.osmonai.com` — Gateway (HTTP API, Telegram webhook) → `gateway:8081`
- `cdn.osmonai.com` — Telegram Mini App (vpn_next, Next.js standalone) → `vpn-next:3000`

Оба автоматизированы одинаково (один Caddy, два виртуальных хоста, оба env-driven).  
**DNS:** сторонний провайдер (namecheap/reg.ru/др.), **не** Cloudflare. Поэтому идём на HTTP-01 валидацию (порт 80), а не DNS-01.

---

## 📚 Контекст / почему отходим от плана

В [02-mvp-c-implementation.md § 9.2](./02-mvp-c-implementation.md) утверждён Cloudflare Tunnel. Этот пункт пересматриваем:

| Причина | Детали |
|---|---|
| NS домена не на Cloudflare | Перенос NS — отдельная задача + 24-48ч propagation. Хочется избежать. |
| CF может срезать VPN-трафик | VPN в серой зоне ToS; одно жалоб-письмо — и туннель отключён. |
| Российские пользователи | Доступ к CF-endpoint-ам из РФ нестабильный (некоторые ASN режут CF). |
| Смена домена | В CF tunnel'е — правка `cloudflared/config.yml` + `cloudflared tunnel route dns` + рестарт. Не хуже, но и не проще. |

Принятое решение — **direct VPS + Caddy reverse proxy** (см. сравнительная таблица в истории обсуждения от 2026-04-23).

---

## 🏗 Архитектура

```
                          Internet
                              │
                 ┌────────────┴────────────┐
                 │  api.osmonai.com (A)    │  ← DNS → IP VPS
                 └────────────┬────────────┘
                              │ 80/tcp (ACME HTTP-01 + redirect → 443)
                              │ 443/tcp (HTTPS)
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│  VPS (Hetzner/Contabo)                                           │
│                                                                  │
│  ┌──────────────────────────────────────────────────────┐       │
│  │  caddy (container)                                   │       │
│  │    listens: 80, 443                                  │       │
│  │    auto-TLS: Let's Encrypt HTTP-01                   │       │
│  │    reverse_proxy gateway:8081                        │       │
│  └──────────┬───────────────────────────────────────────┘       │
│             │ http://gateway:8081  (compose-network "vpn")       │
│             ▼                                                    │
│  ┌──────────────────────────────────────────────────────┐       │
│  │  gateway (:8081, HTTP)    ← as-is                    │       │
│  │  auth/sub/vpn/payment (gRPC)                         │       │
│  │  postgres                                            │       │
│  │  xray  (:8443 VLESS+Reality — НЕ трогаем Caddy)      │       │
│  └──────────────────────────────────────────────────────┘       │
└─────────────────────────────────────────────────────────────────┘
```

**Важно про порты:** Xray слушает `:8443` (VPN_XRAY_VLESS_PORT, Reality под SNI github.com) — Caddy его не видит и не трогает. Только `:80` и `:443` (для api) уходят в Caddy. Конфликта нет.

---

## 🧩 Изменения

### 1. `deploy/compose/docker-compose.yml` — добавить сервис `caddy`

```yaml
  caddy:
    image: caddy:2-alpine
    container_name: vpn-caddy
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
      - "443:443/udp"           # HTTP/3
    environment:
      API_DOMAIN: ${API_DOMAIN}           # ← api.osmonai.com, из .env
      ACME_EMAIL: ${ACME_EMAIL}           # ← email для LE (уведомления об истечении)
      GATEWAY_UPSTREAM: gateway:${GATEWAY_HTTP_PORT}
    volumes:
      - ./caddy/Caddyfile:/etc/caddy/Caddyfile:ro
      - vpn_caddy_data:/data              # сюда LE кладёт сертификаты (persist!)
      - vpn_caddy_config:/config
    depends_on:
      - gateway
    networks: [vpn]

volumes:
  vpn_caddy_data:
  vpn_caddy_config:
```

### 2. `deploy/compose/caddy/Caddyfile` (новый)

```caddy
{
    email {$ACME_EMAIL}
    # auto-HTTPS включён по умолчанию (staging временно — см. §5)
}

{$API_DOMAIN} {
    encode gzip zstd

    # Отдельный health-check для внешнего мониторинга (UptimeRobot)
    @health path /health /api/v1/healthz
    handle @health {
        reverse_proxy {$GATEWAY_UPSTREAM}
    }

    # Всё остальное — в gateway
    reverse_proxy {$GATEWAY_UPSTREAM} {
        header_up X-Real-IP {remote_host}
        header_up X-Forwarded-For {remote_host}
        header_up X-Forwarded-Proto {scheme}
    }

    # Security headers
    header {
        Strict-Transport-Security "max-age=31536000; includeSubDomains"
        X-Content-Type-Options "nosniff"
        Referrer-Policy "strict-origin-when-cross-origin"
        -Server
    }

    log {
        output stdout
        format json
    }
}
```

**Каждая переменная `{$...}` — из env-а контейнера.** Смена домена = правка `.env`, `docker compose up -d caddy`. Перевыпуск сертификата Caddy сделает сам при первом запросе на новый домен (если CAA-записи и A-запись уже указывают на VPS).

### 3. `deploy/env/.env.template` — добавить

```bash
# === Public HTTPS endpoint (Caddy auto-TLS) ===
API_DOMAIN=api.osmonai.com
ACME_EMAIL=admin@osmonai.com
```

### 4. `deploy/compose/docker-compose.yml` — **убрать publish `:8081`** у gateway

Сейчас:
```yaml
gateway:
  ports:
    - "${GATEWAY_HTTP_PORT}:${GATEWAY_HTTP_PORT}"   # 8081 наружу
```

Должно стать:
```yaml
gateway:
  expose:
    - "${GATEWAY_HTTP_PORT}"                        # только внутри compose-сети
```

После этого gateway **недоступен напрямую из интернета** — только через Caddy. Минус потенциальная дыра.

### 5. Staging → Production переключение

Первые 1-2 запуска — **использовать Let's Encrypt Staging**, чтобы не упереться в rate-limit (5 сертов/неделя на домен). В `Caddyfile` временно:

```caddy
{
    email {$ACME_EMAIL}
    acme_ca https://acme-staging-v02.api.letsencrypt.org/directory
}
```

Staging даёт невалидные сертификаты, но процесс полностью идентичен. Как только убедились что всё работает (`curl -k https://api.osmonai.com/health` отвечает) — убираем `acme_ca`, сносим `vpn_caddy_data` (`docker volume rm vpn-stack_vpn_caddy_data`), перезапускаем — получаем реальный prod-серт.

### 6. Taskfile.yaml — пара удобных команд

```yaml
caddy:reload:
  desc: Reload Caddy config without restarting (picks up new env)
  cmds:
    - docker exec vpn-caddy caddy reload --config /etc/caddy/Caddyfile

caddy:logs:
  desc: Tail Caddy logs
  cmds:
    - docker logs -f vpn-caddy

caddy:cert-info:
  desc: Inspect issued cert for $API_DOMAIN
  cmds:
    - docker exec vpn-caddy sh -c 'ls -la /data/caddy/certificates/*/*'

domain:swap:
  desc: 'Swap API domain. Usage: task domain:swap NEW=api.example.com'
  cmds:
    - sed -i.bak "s/^API_DOMAIN=.*/API_DOMAIN={{.NEW}}/" deploy/env/.env
    - docker compose -f deploy/compose/docker-compose.yml up -d caddy
    - echo "Domain swapped to {{.NEW}}. Caddy will issue cert on first request."
```

### 7. VPS firewall (ufw)

Было в § 9.1:
```
ufw allow ssh, 8443/tcp (VLESS)
```

Добавить:
```
ufw allow 80/tcp           # ACME HTTP-01 + auto-redirect на 443
ufw allow 443/tcp          # HTTPS
ufw allow 443/udp          # HTTP/3 (опционально, можно позже)
```

**Убрать:** `8081/tcp` — gateway больше не публикуется.

### 8. Telegram webhook + Mini App URL

Ничего не меняется — всё так же:
- `@BotFather` → Mini App URL: `https://api.osmonai.com`
- `setWebhook`: `https://api.osmonai.com/api/v1/telegram/webhook`

Если домен поменяем — надо не забыть **перенастроить оба места** у `@BotFather` и в ENV `TELEGRAM_WEBHOOK_SECRET`.

---

## ✅ Definition of Done

- [x] `deploy/compose/caddy/Caddyfile` создан (staging-first, валидируется через `caddy validate`)
- [x] `deploy/compose/docker-compose.yml` обновлён (сервис caddy с `profiles: [prod]`, gateway.expose вместо ports)
- [x] `deploy/compose/docker-compose.dev.yml` — dev-оверрайд, публикует gateway на `:8081`
- [x] `.env.template` содержит `API_DOMAIN`, `ACME_EMAIL` (+ синхронизировано в мастер `.env`)
- [x] Taskfile: `compose:up` (dev, без caddy) / `compose:up-prod` (с caddy) / `caddy:up` / `caddy:reload` / `caddy:restart` / `caddy:logs` / `caddy:cert-info` / `caddy:go-prod` / `domain:swap` / `domain:swap-cdn`
- [x] **vpn_next dockerized**: `vpn_next/Dockerfile` (multi-stage, `output: 'standalone'`), `.dockerignore`, образ билдится за ~20с, runtime ~305MB
- [x] Caddyfile: второй виртуальный хост `cdn.osmonai.com` → `vpn-next:3000` с long-term кэшем для `/_next/static/*`
- [x] `CDN_DOMAIN` в env-template/master, `GATEWAY_URL=http://gateway:8081/api/v1` проброшен в vpn-next container (для `/api/proxy`)
- [x] Taskfile: `next:build` / `next:up` / `next:restart` / `next:logs`
- [ ] Запустили на VPS, получили staging-серт для **обоих** доменов, валидация работает (`curl -k https://api.osmonai.com/health`, `curl -k https://cdn.osmonai.com/`)
- [ ] `task caddy:go-prod` — переключение на production LE, `curl https://api.osmonai.com/health` → 200 с валидным сертом (без `-k`)
- [ ] Gateway **недоступен** на `:8081` извне: `curl http://<vps-ip>:8081/health` → connection refused (проверяется в prod без dev-override)
- [ ] Mini App в Telegram работает через новый домен
- [ ] Smoke-test `task domain:swap NEW=api.osmonai.dev` (для DNS, которые указывают на тот же VPS) — Caddy автоматом выпускает новый серт в течение 30 секунд
- [ ] Обновлены § 9.2 и § 9.7 в `02-mvp-c-implementation.md` со ссылкой сюда

---

## ⚠️ Риски и ограничения

| Риск | Смягчение |
|---|---|
| IP VPS засветится в whois/DNS | Принимаем: backend публичный, Xray всё равно маскируется через Reality |
| LE rate-limit (5 certs/week per domain) | Staging для всех тестов (см. §5); для смены домена — не более 5 новых доменов в неделю |
| Порт 80 необходим для HTTP-01 (ACME) | Открыт в ufw; auto-redirect на 443 |
| Реестр РКН → блок домена | Меняем `API_DOMAIN`, Caddy автомат. выпускает новый серт. Ради этого вся затея. |
| DDoS на публичный IP | Fail2ban + rate-limit в Caddy; на проде — подключить CF proxy с whitelist IP VPS |
| Caddy упал → весь API недоступен | `restart: unless-stopped` + healthcheck (можно добавить) |

---

## 🤔 Открытые вопросы

1. **wildcard серт (`*.osmonai.com`)?** Если планируется `app.osmonai.com` (frontend на том же VPS) + `api.osmonai.com` — лучше сразу wildcard. Но для wildcard нужна **DNS-01** валидация, а DNS не на CF. Варианты:
   - Каждый поддомен — отдельный сертификат (5 sub × 1 cert = 5 LE-запросов, нормально).
   - Перенести NS на Cloudflare (только DNS, без proxy) — тогда wildcard + все остальные плюсы.
2. **Frontend (vpn_next).** Сейчас в § 9.3 предложен Vercel. Если хостим на том же VPS — Caddy добавит блок `app.osmonai.com { reverse_proxy vpn-next:3000 }`, никаких проблем.
3. **Rate-limit в Caddy.** `caddy-rate-limit` — community-плагин, не входит в core image. Надо либо собрать кастомный `caddy` binary (через xcaddy), либо вынести rate-limit в сам Gateway (тогда `caddy-rate-limit` не нужен) — пересекается с Cross-cutting TODO «Rate limiting в Gateway».
4. **Staging → Prod автоматизация.** Сейчас переключение руками. Можно сделать env-флаг `ACME_STAGING=1/0` и `Caddyfile` со шаблоном (`{$ACME_CA}`), но это overkill для первого деплоя.

---

## 🔗 Ссылки

- [Caddy docs — Automatic HTTPS](https://caddyserver.com/docs/automatic-https)
- [Caddy docs — On-Demand TLS](https://caddyserver.com/docs/automatic-https#on-demand-tls) — на случай «любой домен, не знаю заранее»
- [Let's Encrypt rate limits](https://letsencrypt.org/docs/rate-limits/)
- Родительский план: [02-mvp-c-implementation.md § 9](./02-mvp-c-implementation.md), пункты 9.2 и 9.7 заменяются этой задачей
- Связанный TODO: Cross-cutting «Rate limiting в Gateway»
