# Xray ↔ БД: как связаны

Этот документ объясняет, как **VPN Service**, **Xray** и **Postgres** работают вместе — где что хранится, как синхронизируется, и почему так сделано.

---

## 🧠 Простыми словами

Представь **ночной клуб**:

- **Xray** = вышибала на входе. Знает только список UUID-ов «кого пускать». Не помнит кто купил, когда кончается подписка, на сколько устройств — ему это не нужно.
- **Postgres (БД)** = офис клуба. Хранит всё: кто юзер, что купил, когда истекает, сколько устройств разрешено.
- **VPN Service (Go)** = менеджер. Ходит между офисом и вышибалой — «запиши этого в список», «убери этого».

**Xray ничего не читает из БД. VPN Service ничего не хранит у Xray.** Между ними только **gRPC API Xray** — команды `AddUser` / `RemoveUser` / `QueryStats`.

---

## 🏗️ Архитектура (кто с кем общается)

```
┌─────────────────────────────────────────────────────────────────────┐
│  📱 Клиент (xray-client на телефоне / PC / в Docker)                 │
│     VLESS-ссылка:                                                    │
│       vless://UUID@host:port?pbk=…&sid=…&sni=github.com              │
└────────────────────────────────┬────────────────────────────────────┘
                                 │ 1. VLESS + Reality handshake
                                 │    (TCP 8443, чистый TLS-mimicry)
                                 ▼
┌─────────────────────────────────────────────────────────────────────┐
│  🛡️  Xray (контейнер vpn-xray, образ ghcr.io/xtls/xray-core)         │
│                                                                      │
│  ┌───────────────────────────────────────────────────────┐          │
│  │ inbound "vless-reality-in" (TCP 8443)                 │          │
│  │   clients: [                                          │          │
│  │     { uuid: "d9c9ce47-…", email: "user1@vpn.local" }  │◄──┐      │
│  │     { uuid: "aaaa-…",     email: "user2@vpn.local" }  │   │      │
│  │   ]                          ^                        │   │      │
│  │                              │ in-memory! при рестарте│   │      │
│  │                              │   Xray — список чистый │   │      │
│  └───────────────────────────────────────────────────────┘   │      │
│                                                              │      │
│  ┌───────────────────────────────────────────────────────┐   │      │
│  │ inbound "api" (TCP 10085, dokodemo-door)              │   │      │
│  │   ├── HandlerService.AlterInbound ◄── AddUser/Remove ─┼───┤      │
│  │   └── StatsService.QueryStats     ◄── счётчик трафика ┤   │      │
│  └───────────────────────────────────────────────────────┘   │      │
└────────────────────────────────┬─────────────────────────────┼──────┘
                                 │ 2. gRPC API                 │
                                 │    ("добавь UUID в inbound")│
                                 ▼                             │
┌─────────────────────────────────────────────────────────────────────┐
│  ⚙️  VPN Service (контейнер vpn-core, сервис на :50062)              │
│                                                                      │
│  CreateVPNUser(user_id=1, sub_id=1):                                 │
│    ① INSERT vpn_users (user_id, subscription_id, uuid, email, …)    │
│         └─► Postgres                                                 │
│    ② SELECT * FROM vpn_servers WHERE is_active                       │
│         ◄─ Postgres                                                  │
│    ③ for each server: xray.AddUser(inbound_tag, uuid, email) ────────┘
│         ─► Xray API
└────────────────────────────────┬────────────────────────────────────┘
                                 │ 3. SQL (pgxpool)
                                 ▼
┌─────────────────────────────────────────────────────────────────────┐
│  🗄️  Postgres (контейнер vpn-postgres)                               │
│                                                                      │
│  users               — кто, telegram_id, роль, баланс                │
│  subscriptions       — какой тариф, когда истекает, max_devices      │
│  vpn_users           — UUID + email на подписку (ТО ЖЕ в Xray RAM)   │
│  vpn_servers         — куда подключаться + Reality-ключи + inbound_tag│
│  active_connections  — живые коннекты (heartbeat по Xray stats)      │
└─────────────────────────────────────────────────────────────────────┘
```

---

## 🗂️ Что где лежит (маппинг полей)

| Данные | В Postgres | В Xray (памяти) | Откуда берётся |
|---|---|---|---|
| UUID клиента | `vpn_users.uuid` | `inbound.clients[i].id` | VPN Service сгенерил (UUIDv4) |
| Email-тег | `vpn_users.email` | `inbound.clients[i].email` | `user{id}@vpn.local` |
| Flow | `vpn_users.flow` | `inbound.clients[i].flow` | `xtls-rprx-vision` (Reality требует) |
| Имя inbound'а | `vpn_servers.inbound_tag` | `inbound.tag` | `VPN_XRAY_INBOUND_TAG` из env |
| Хост сервера | `vpn_servers.host` | — (Xray не знает своего внешнего имени) | `VPN_XRAY_PUBLIC_HOST` из env |
| VLESS порт | `vpn_servers.port` | `inbound.port` | `VPN_XRAY_VLESS_PORT` из env |
| Reality keys | `vpn_servers.public_key` | `realitySettings.privateKey` | `task xray:genkeys` → env |
| Reality short_id | `vpn_servers.short_id` | `realitySettings.shortIds[0]` | `openssl rand -hex 8` → env |
| SNI / Dest | `vpn_servers.server_names`, `.dest` | `realitySettings.serverNames`, `.dest` | `github.com` / `github.com:443` |
| Подписка | `subscriptions` | — | Xray вообще не знает |
| Трафик | `active_connections.last_seen` (TODO) | `stats.user>>>email>>>traffic` | Xray считает → VPN Service забирает |

**Главное:** Xray **дублирует** только `uuid`, `email`, `flow` из БД в своей RAM. Всё остальное (подписка, срок, лимит устройств, реферальная программа) — **только** в БД.

---

## 🔄 Жизненный цикл юзера

### 1. Рождение (оплатил подписку)
```
Telegram Mini App ─► POST /api/v1/payments/success  (на Этапе 5)
                            │
                            ▼
           Subscription Service: INSERT subscriptions
                            │
                            ▼
           VPN Service.CreateVPNUser(user_id, sub_id):
              ① INSERT vpn_users (uuid=UUIDv4, email=user{id}@vpn.local)
                    ─► Postgres
              ② SELECT * FROM vpn_servers WHERE is_active
                    ◄─ Postgres
              ③ for each server:
                   xray.AddUser(inbound_tag, uuid, email, flow="xtls-rprx-vision")
                                                            ─► Xray gRPC API
                                                                   │
                       ┌───────────────────────────────────────────┘
                       ▼
             Xray кладёт { uuid, email, flow } в
             inbound "vless-reality-in" (в RAM).
             Теперь handshake с этим UUID будет успешным.
```

### 2. Жизнь (клиент подключается)
```
📱 ──VLESS+Reality──► Xray: "я UUID=d9c9ce47…"
                      │
                      ├─ Xray ищет в своей памяти → найден
                      ├─ Проверяет Reality handshake (pbk, sid, sni) → ок
                      └─ Открывает TCP-туннель наружу (freedom outbound)

БД тут вообще не участвует. Xray сам всё решает за микросекунды.
```

### 3. Сбор статистики (✅ Этап 3, реализовано)
```
Каждые 60 секунд, горутина Heartbeat (service/heartbeat.go):
  for each vpn_user:
    stats = xray.GetUserStats(email, reset=false)     ─► Xray Stats API
    total = stats.Uplink + stats.Downlink
    if total > prevSeen[email]:                      (in-memory map)
      UPDATE active_connections SET last_seen=NOW()
      WHERE vpn_user_id = X
    prevSeen[email] = total

  — UPDATE бьётся сразу по ВСЕМ device_identifier юзера,
    т.к. один UUID даёт один счётчик (см. docs/services/device-limit.md)
```

### 4. Смерть (подписка истекла / бан)
```
Cron в VPN Service (по истечении подписки):
  ① xray.RemoveUser(inbound_tag, email)   ─► Xray убрал из памяти
  ② DELETE FROM vpn_users ...             ─► Postgres очистил

Следующий handshake с этим UUID → Xray скажет "unknown user"
                                 → connection reset.
```

### 5. Рестарт Xray (⚠️ важно)
```
Xray рестартует (обновление, крэш) → теряет ВСЁ из inbound.clients[].
                                      (config.json содержит clients: [])
   │
   └─► При рестарте VPN Service должен делать re-seed:
       SELECT vpn_users WHERE ... → xray.AddUser(...) для каждого

   Сейчас TODO. Пока рестарт Xray → все юзеры отвалились до re-CreateVPNUser.
```

---

## 🤔 Почему так сделано?

**Xray держит clients в памяти, не в БД — это осознанно:**
- Handshake должен быть микросекундным → заглядывать в БД на каждом коннекте = смерть производительности
- Xray — stateless по дизайну; при сбое просто перезаливаем из источника правды
- **БД — источник правды**, **Xray — рабочая копия**. Мы всегда можем восстановить Xray из БД, но не наоборот.

**`inbound_tag` — это ключ связи:**
- Один Xray-контейнер может держать 10 inbound'ов (разные страны, протоколы, порты)
- В `vpn_servers.inbound_tag` пишем имя конкретного inbound'а в конкретном Xray
- `xray.AddUser(tag, …)` знает куда именно добавить — к нужному inbound'у этого сервера

**Reality keys хранятся в двух местах, и это ок:**
- В `config.json` Xray — `privateKey` + `shortIds` (без них Reality не запустится)
- В `vpn_servers` БД — `public_key` + `short_id` (чтобы VPN Service мог отдать их клиенту в VLESS-ссылке)
- Оба источника заполняются из одного env (`VPN_XRAY_REALITY_*`) через `task env:generate` → drift невозможен

**Что синхронизируется автоматически, что нет:**
- ✅ `CreateVPNUser` — сразу в Xray (Этап 2)
- ✅ Трафик → `active_connections.last_seen` через Heartbeat каждые 60с (Этап 3)
- ✅ Выдача VLESS-ссылки → UPSERT записи в `active_connections` + проверка `max_devices` (Этап 3)
- ❌ Истечение подписки → физическое удаление из Xray — **TODO Этап 5**
- ❌ Рестарт Xray → re-seed всех существующих юзеров — **TODO**

---

## 🗺️ Где это в коде

| Файл | Что делает |
|---|---|
| [`platform/pkg/xray/client.go`](../../platform/pkg/xray/client.go) | Go-клиент Xray gRPC API: `New`, `AddUser`, `RemoveUser`, `GetUserStats` |
| [`services/vpn-service/internal/service/vpn.go`](../../services/vpn-service/internal/service/vpn.go) | `CreateVPNUser` — создаёт запись в БД + цикл `xray.AddUser(...)` по всем активным серверам |
| [`services/vpn-service/internal/app/app.go`](../../services/vpn-service/internal/app/app.go) | `initXray` (dial Xray API), `seedLocalServer` (upsert локального сервера при старте) |
| [`services/vpn-service/migrations/002_add_inbound_tag_and_clear_seed.up.sql`](../../services/vpn-service/migrations/002_add_inbound_tag_and_clear_seed.up.sql) | Добавила `vpn_servers.inbound_tag` + UNIQUE(name) для upsert |
| [`deploy/compose/xray/config.json.template`](../../deploy/compose/xray/config.json.template) | Шаблон Xray-конфига. Плейсхолдеры `${VPN_XRAY_REALITY_PRIVATE_KEY}` и т.д. подставляются envsubst'ом при `task env:generate` |
| [`deploy/compose/docker-compose.yml`](../../deploy/compose/docker-compose.yml) | Сервис `xray` + `depends_on: vpn-service → xray` |
| [`deploy/env/.env.template`](../../deploy/env/.env.template) | Мастер: Reality keys, short_id, inbound tag, публичный хост |

---

## 🧪 Как это воспроизвести (e2e)

Полный путь «создали юзера через API → клиент подключился через Reality → пошёл реальный трафик»:

```bash
# 1. Поднять стек (postgres + xray + vpn-core + auth + sub + gateway)
task compose:up

# 2. Создать тестового юзера и подписку в БД напрямую
docker exec vpn-postgres psql -U vpn -d vpn -c \
  "INSERT INTO users (telegram_id, first_name) VALUES (12345, 'E2E') \
   ON CONFLICT (telegram_id) DO UPDATE SET first_name='E2E' RETURNING id;"

docker exec vpn-postgres psql -U vpn -d vpn -c \
  "INSERT INTO subscriptions (user_id, plan_id, max_devices, total_price, \
      started_at, expires_at, status) \
   SELECT id, 1, 2, 199, NOW(), NOW() + INTERVAL '30 days', 'active' \
   FROM users WHERE telegram_id = 12345;"

# 3. Через grpcurl вызвать CreateVPNUser (VPN Service регистрирует юзера в Xray)
docker run --rm --network vpn-stack_vpn fullstorydev/grpcurl:latest \
  -plaintext -d '{"user_id": 1, "subscription_id": 1}' \
  vpn-core:50062 vpn.v1.VPNService/CreateVPNUser

# → вернёт UUID, например "d9c9ce47-812b-4c8d-95e7-b97482dd6f2d"

# 4. Получить VLESS-ссылку
docker run --rm --network vpn-stack_vpn fullstorydev/grpcurl:latest \
  -plaintext -d '{"user_id": 1, "server_id": 5}' \
  vpn-core:50062 vpn.v1.VPNService/GetVLESSLink

# 5. Проверить что юзер реально попал в Xray — через логи
docker logs vpn-core 2>&1 | grep "xray user added"
#  → {"msg":"xray user added","user_id":1,"server_id":5,"inbound_tag":"vless-reality-in"}

# 6. Поднять xray-client в соседнем контейнере с этим UUID
#    (см. deploy/compose/xray/README.md — клиентский config)
#    и сделать curl --socks5 через него на ipinfo.io
#    → увидишь IP хоста, на котором крутится vpn-xray
```

---

## 🔧 Дебаг / траблшутинг

### Юзер добавился в БД, но Xray не принимает коннект

```bash
# Посмотреть логи VPN Service
docker logs vpn-core 2>&1 | grep -iE 'xray|user'

# Посмотреть логи Xray (запрос на API inbound должен быть)
docker logs vpn-xray 2>&1 | tail -20
#  → ждём строчку "accepted tcp:127.0.0.1:10085 [api -> api]"

# Проверить что VPN Service вообще достучался до Xray
docker exec vpn-core nc -zv xray 10085
```

### Xray рестартовал, юзеры отвалились

Временно (пока нет авто re-seed): просто позови `CreateVPNUser` заново — но он зафейлится из-за UNIQUE(user_id, subscription_id) на `vpn_users`. Правильно: добавить `task re-seed-xray` (TODO).

### Поменяли Reality keys в env — клиенты отвалились

```bash
# Пересобрать config.json (подставит новые ключи)
task env:generate

# Рестартовать Xray
task xray:restart

# VPN Service уже при старте обновит vpn_servers.public_key из env (seedLocalServer)
task compose:restart   # или просто docker compose restart vpn-service
```

### grpcurl не подключается к vpn-xray:10085

На Xray **нет** gRPC reflection (это особенность xray-core). Нужны .proto-файлы для вызова API извне. Используй VPN Service (у него reflection включён) — он сделает правильный вызов.

---

## 📚 См. также

- [01-mvp-plan.md](../tasks/01-mvp-plan.md) — общий план MVP
- [02-mvp-c-implementation.md](../tasks/02-mvp-c-implementation.md) — детальный прогресс (Этапы 1, 2 закрыты)
- [ARCHITECTURE.md](../ARCHITECTURE.md) — общая архитектурная диаграмма
- [Официальная дока Xray REALITY](https://github.com/XTLS/Xray-core/discussions/1708)
