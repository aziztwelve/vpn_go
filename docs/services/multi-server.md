# Multi-server архитектура

Как работает горизонтальное масштабирование: добавили новый Xray VPS → все юзеры автоматически получают к нему доступ без простоя.

**Статус реализации:** ✅ **реализовано (backend), на MVP 1 сервер** (Этап 6, 2026-04-22)

---

## 🧠 Простыми словами

Чем больше юзеров — тем больше нагрузка на Xray. Решение — **добавить 2-й, 3-й сервер** (другой географии или просто для распределения). Это называется **горизонтальное масштабирование**.

**Два подхода к multi-server VPN:**
- **A) Один UUID на юзера, разные серверы** ← мы выбрали это
  - Каждый юзер имеет **один** UUID, который прописан во **всех** Xray inbound'ах
  - Клиент выбирает сервер в UI → получает VLESS-ссылку для этого сервера с тем же UUID
  - Простая модель, простой биллинг
- **B) UUID per сервер**
  - Юзер имеет разные UUID на разных серверах
  - Сложнее, но даёт тонкий контроль (банить на одном, не трогая других)
  - Не в MVP

---

## 🏗️ Архитектура

```
┌──────────────────────────────────────────────────────────────────┐
│  📱 Mini App                                                      │
│                                                                   │
│  1. GET /api/v1/vpn/servers → список { id, name, location,        │
│     country_code, load_percent, is_active }                       │
│                                                                   │
│  2. Юзер выбирает сервер → GET /api/v1/vpn/servers/:id/link       │
│     → vless://UUID@host:port — ссылка для КОНКРЕТНОГО сервера     │
└────────────────────────────┬─────────────────────────────────────┘
                             │
                             ▼
┌──────────────────────────────────────────────────────────────────┐
│  ⚙️  VPN Service                                                  │
│                                                                   │
│  CreateVPNUser (после оплаты):                                    │
│    for each server in vpn_servers WHERE is_active:                │
│      xray.AddUser(inbound_tag, uuid, email, flow)  ─┐             │
│    — Best-effort partial success. Если хоть один    │             │
│      сервер принял → юзер зарегистрирован.          │             │
│                                                      │             │
│  LoadCron (каждые 60с):                             │             │
│    for each active server:                          │             │
│      UPDATE vpn_servers SET load_percent =          │             │
│        (count active_connections since 5min)        │             │
│        * 100 / server_max_connections                │             │
│                                                      │             │
│  ResyncServer (при масштабировании):                │             │
│    for each user in vpn_users:                      │             │
│      xray.AddUser на новый server inbound           │             │
└──────────────────────────────────────────────────────┼────────────┘
                                                       │ gRPC
                                                       ▼
┌──────────────────────────────────────────────────────────────────┐
│  🛡️  Xray servers (N штук)                                        │
│                                                                   │
│  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐│
│  │ Local (dev)      │  │ Germany-01       │  │ Japan-01         ││
│  │ inbound_tag:     │  │ inbound_tag:     │  │ inbound_tag:     ││
│  │ vless-reality-in │  │ vless-reality-in │  │ vless-reality-in ││
│  │ clients: [       │  │ clients: [       │  │ clients: [       ││
│  │   uuid-A,        │  │   uuid-A,        │  │   uuid-A,        ││
│  │   uuid-B,        │  │   uuid-B,        │  │   uuid-B,        ││
│  │   ...            │  │   ...            │  │   ...            ││
│  │ ]                │  │ ]                │  │ ]                ││
│  └──────────────────┘  └──────────────────┘  └──────────────────┘│
│   Все серверы имеют ОДИНАКОВЫЙ набор clients (один UUID — все).   │
└──────────────────────────────────────────────────────────────────┘
```

---

## 🔑 Ключевые данные

### `vpn_servers` таблица (после миграции 003)
| Колонка | Что |
|---|---|
| `id`, `name` | PK + человекочитаемое имя (UNIQUE) |
| `location`, `country_code` | "Frankfurt", "DE" — для UI |
| `host`, `port` | что идёт в VLESS-ссылку юзеру |
| `public_key`, `private_key`, `short_id` | Reality keys этого сервера (разные у каждого VPS) |
| `dest`, `server_names` | куда маскируется Reality (обычно github.com) |
| `xray_api_host`, `xray_api_port` | куда VPN Service коннектится по gRPC (`AlterInbound`, `QueryStats`) |
| `inbound_tag` | имя inbound'а внутри Xray config.json (у нас везде `vless-reality-in`) |
| `is_active` | флаг "сервер доступен для CreateVPNUser / выдачи ссылок" |
| `load_percent` | `0..100`, обновляется LoadCron каждые 60с |
| `server_max_connections` | capacity для расчёта `load_percent` (default 1000) |
| `description` | "Hetzner 4GB / 10 Gbit/s" — для UI |

---

## ⚙️ Компоненты

### 1. CreateVPNUser с partial success
```go
// services/vpn-service/internal/service/vpn.go
for each server in vpn_servers WHERE is_active:
    xray.AddUser(inbound_tag, uuid, email, flow)
        if "already exists" → treat as success
        if error → log, continue (не роняем запрос)

if servers_ok == 0 && len(servers) > 0:
    return error "failed on any server"
```

**Почему так:** если 1 из 3 серверов недоступен, оплатившему юзеру важнее получить VLESS на 2 работающих, чем ждать пока 3-й починится. Незакрытый сервер восстанавливается при `ResyncServer`.

### 2. LoadCron (каждые 60с)
```go
// services/vpn-service/internal/service/load_cron.go
for each server_id in ListActiveServerIDs:
    UPDATE vpn_servers SET load_percent = 
        LEAST(100, GREATEST(0,
            COUNT(active_connections.last_seen > NOW()-5min) * 100
            / server_max_connections
        ))
```

**Зачем:** UI показывает `load_percent` → юзер видит "Франкфурт 80%, Токио 20%" и выбирает менее нагруженный. Для балансировщика.

### 3. ResyncServer — добавление нового сервера
```go
// vpn-service.ResyncServer(server_id) gRPC
for each user in vpn_users:
    xray.AddUser на inbound_tag этого server
    "already exists" → idempotent skip
```

**Зачем:** когда добавляется новый VPS, у Xray пустой `clients: []`. Чтобы существующие юзеры сразу им могли пользоваться, надо их все прописать.

### 4. deploy-xray-new.sh
Скрипт генерирует:
- Свежие Reality keys + short_id
- `config.json` для нового VPS
- SQL `INSERT INTO vpn_servers`
- grpcurl команду `ResyncServer`
- Пошаговую инструкцию

---

## 🎬 Сценарий: добавление 3-го сервера

Предположим, у нас работают `Local (dev)` и `Germany-01`, хотим добавить `Japan-01`:

```bash
# 1. Генерация ключей + config.json на локальной машине
./deploy/scripts/deploy-xray-new.sh \
    --name "Japan-01" \
    --location "Tokyo" \
    --country JP \
    --host "jp01.maydavpn.com" \
    --port 443 \
    --max-conn 2000 \
    --description "Tokyo, JP · 10 Gbit/s"

# → выведет ключи, config.json, SQL и grpcurl-команду

# 2. Запустить Xray на VPS jp01.maydavpn.com (команды в выводе скрипта)
scp -r deploy/compose/xray-new/Japan_01 root@jp01.maydavpn.com:/opt/xray
ssh root@jp01.maydavpn.com 'docker run -d --name xray \
    --restart unless-stopped \
    -v /opt/xray/config.json:/etc/xray/config.json:ro \
    -p 443:443 -p 10085:10085 \
    ghcr.io/xtls/xray-core:latest -c /etc/xray/config.json'

# 3. INSERT в БД (на backend VPS)
docker exec -i vpn-postgres psql -U vpn -d vpn < insert.sql
# → id: 3

# 4. ResyncServer: прописать всех существующих юзеров в inbound сервера 3
docker run --rm --network vpn-stack_vpn fullstorydev/grpcurl:latest \
    -plaintext -d '{"server_id": 3}' \
    vpn-core:50062 vpn.v1.VPNService/ResyncServer
# → {"usersTotal": 152, "usersAdded": 152, "usersAlready": 0, "usersFailed": 0}

# 5. Готово! Новый сервер работает, load_percent обновится через 60с.
```

**Downtime: 0.** Никакой рестарт backend-сервисов не нужен.

---

## ⚠️ Ограничения текущей модели

1. **Один UUID на юзера** — значит нельзя забанить юзера только на одном сервере, только глобально через `DisableVPNUser`. Для multi-server hard-control нужен UUID per server (не MVP).
2. **ResyncServer блокирующий** — при 10к юзеров это 10к xray.AddUser подряд. При добавлении нового сервера с большой юзер-базой — секунды-минуты. Для MVP норма, в будущем можно параллелить.
3. **load_percent округляется вниз** (integer division). 1 юзер на сервере с max=1000 → load_percent=0. Это ок для UI, реальный расчёт — для мониторинга/балансировки достаточной точности.
4. **Balancing ручной** — юзер сам выбирает сервер в UI. Auto-выбор "самого ненагруженного" — логика фронтенда (из `load_percent` поля). В будущем — load-aware routing.

---

## 🗺️ Где это в коде

| Файл | Что |
|---|---|
| [`services/vpn-service/migrations/003_add_server_capacity.up.sql`](../../services/vpn-service/migrations/003_add_server_capacity.up.sql) | `+server_max_connections INT DEFAULT 1000`, `+description` |
| [`services/vpn-service/internal/service/vpn.go`](../../services/vpn-service/internal/service/vpn.go) | `CreateVPNUser` с best-effort partial success, `ResyncServer` |
| [`services/vpn-service/internal/service/load_cron.go`](../../services/vpn-service/internal/service/load_cron.go) | LoadCron (тикер 60с) |
| [`services/vpn-service/internal/repository/vpn.go`](../../services/vpn-service/internal/repository/vpn.go) | `UpdateServerLoad`, `ListActiveServerIDs`, Scan с новыми колонками |
| [`services/vpn-service/internal/api/vpn.go`](../../services/vpn-service/internal/api/vpn.go) | `ResyncServer` handler |
| [`shared/proto/vpn/v1/vpn.proto`](../../shared/proto/vpn/v1/vpn.proto) | `+ResyncServer` RPC + `+ResyncServerRequest/Response` |
| [`deploy/scripts/deploy-xray-new.sh`](../../deploy/scripts/deploy-xray-new.sh) | Скрипт добавления нового Xray VPS (генерация ключей → config.json → SQL + resync) |

---

## 📚 См. также

- [xray-integration.md](./xray-integration.md) — базовый флоу Xray ↔ БД
- [device-limit.md](./device-limit.md) — как считаются active_connections (основа для load_percent)
- [01-mvp-plan.md](../tasks/01-mvp-plan.md) — план MVP
