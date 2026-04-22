# Device Limit — лимит одновременных устройств

Этот документ объясняет, как работает ограничение `max_devices` подписки — почему оно не на уровне Xray, где "живёт" счётчик, и как устроен heartbeat.

**Статус реализации:** 🔵 в работе (Этап 3 плана MVP)

---

## 🤔 Задача

Юзер купил подписку с `max_devices = 2`. Нужно не дать ему подключить 3-е устройство.

**Подвох:** Xray **не знает про устройства**. У юзера один UUID — Xray просто «пускает кого угодно с этим UUID». Можно раздать этот UUID хоть 100 друзьям, Xray всех пустит.

Значит лимит надо делать **логически** — на уровне VPN Service, до того как Xray вообще увидит клиента.

---

## 🎟️ Аналогия — «бейджи на входе»

```
📜 Подписка (max_devices=2)
   = «право выдать 2 бейджа»

🎫 active_connections (одна строка = один бейдж)
   = «бейдж, привязанный к device_identifier: iPhone / PC / Tablet»

👤 Юзер жмёт "Подключить iPhone" в Mini App
   → VPN Service смотрит: «свободных бейджей < 2?»
   → Да → выдаёт бейдж и VLESS-ссылку
   → Нет → отказ "device limit exceeded"

⏱ Бейдж "протухает" если устройство не гнало трафик 5 минут
   → бейдж возвращается в пул → можно выдать новый
```

Лимит стоит **на выдаче VLESS-ссылки**, а не на подключении. Это soft-limit — достаточно для MVP. Для hard-limit (как в NordVPN) нужен отдельный UUID на устройство — оставили на потом.

---

## 🔄 Три движущиеся части

```
┌──────────────────────────────────────────────────────────────┐
│  ЧАСТЬ 1 — Выдача ссылки (синхронно, по запросу юзера)       │
│                                                               │
│  POST /api/v1/vpn/devices { name: "iPhone" }                  │
│         │                                                     │
│         ▼                                                     │
│  SELECT COUNT(*) FROM active_connections                      │
│    WHERE vpn_user_id = X                                      │
│      AND last_seen > NOW() - INTERVAL '5 minutes'             │
│         │                                                     │
│         ├─ < max_devices → INSERT строчку → вернуть VLESS     │
│         └─ >= max_devices → ERROR "device limit exceeded"     │
└──────────────────────────────────────────────────────────────┘
```

```
┌──────────────────────────────────────────────────────────────┐
│  ЧАСТЬ 2 — Heartbeat (фоновая горутина, каждые 60 секунд)    │
│                                                               │
│  for each vpn_user:                                           │
│    stats = xray.GetUserStats(email)                           │
│                        │                                      │
│                        ▼ (Xray Stats API)                     │
│    totalBytes = stats.Uplink + stats.Downlink                 │
│                                                               │
│    if totalBytes > previousTotal[email]:                      │
│      UPDATE active_connections                                │
│      SET last_seen = NOW()                                    │
│      WHERE vpn_user_id = X;                                   │
│      previousTotal[email] = totalBytes                        │
│                                                               │
│  — если счётчик не вырос → юзер не активен → last_seen        │
│    остаётся старым → через 5 мин слот "освобождается"         │
└──────────────────────────────────────────────────────────────┘
```

```
┌──────────────────────────────────────────────────────────────┐
│  ЧАСТЬ 3 — Ручное отключение (юзер в UI жмёт "Отключить PC") │
│                                                               │
│  DELETE /api/v1/vpn/devices/:connection_id                    │
│         │                                                     │
│         ▼                                                     │
│  DELETE FROM active_connections WHERE id = :connection_id;    │
│  // слот сразу свободен                                       │
└──────────────────────────────────────────────────────────────┘
```

---

## 📊 Схема потоков

```
┌─────────────────────────────────────────────────────────────────┐
│  📱 Mini App                                                     │
│                                                                  │
│  1. "Подключить iPhone"                                          │
│      POST /api/v1/vpn/devices?name=iPhone                        │
│      ──────────────────────────────┐                             │
│                                    │                             │
│  2. Получает VLESS-ссылку          │                             │
│      ◄───────────────────────── ссылка готова                    │
└───────────────────────────────────────────────────────────────── │
                                     │                             │
                                     ▼                             │
┌─────────────────────────────────────────────────────────────────┐│
│  ⚙️  VPN Service                                                 ││
│                                                                  ││
│  [Sync]  RequestVLESSLink:                                       ││
│    ① SELECT COUNT active_connections                             ││
│         WHERE last_seen > NOW() - 5min                           ││
│    ② если < max_devices → INSERT active_connection               ││
│    ③ вернуть VLESS-ссылку                                        ││
│                                                                  ││
│  [Async 60s]  PollXrayStats():                                   ││
│    для каждого vpn_user:                                         ││
│    ① GetUserStats(email) → uplink, downlink ◄── Xray Stats API   ││
│    ② если счётчик вырос → UPDATE last_seen = NOW()               ││
└─────────────────────────────────────────────────────────────────┘│
                        │                                          │
                        │ (SQL)                                    │
                        ▼                                          │
┌─────────────────────────────────────────────────────────────────┐│
│  🗄️  Postgres.active_connections                                 ││
│                                                                  ││
│   id │ vpn_user_id │ device_identifier │ connected_at│ last_seen ││
│  ────┼─────────────┼───────────────────┼─────────────┼─────────  ││
│   10 │     5       │ iPhone            │ 12:00       │ 12:03 ←   ││
│   11 │     5       │ PC                │ 12:01       │ 11:55 ⏰  ││
│                                                       (мёртвый!) ││
└─────────────────────────────────────────────────────────────────┘│
                                                                    │
┌─────────────────────────────────────────────────────────────────┐│
│  🛡️  Xray                                                        ││
│                                                                  ││
│  stats.user>>>user5@vpn.local>>>traffic>>>uplink   = 152MB       ││
│                                         >>>downlink = 421MB      ││
│  ──────────────────────────────────────► VPN Service забирает    ││
│                                            каждые 60с            ││
└─────────────────────────────────────────────────────────────────┘│
                                                                    │
                                                                   📱 iPhone
                                                                   (VLESS
                                                                    коннект)
```

---

## 🎬 Примеры сценариев

### ✅ Юзер подключает 1-е устройство (всё ок)
```
active_connections:  [ ]                        (0 записей)
POST /api/v1/vpn/devices?name=iPhone
   → COUNT активных = 0 < max_devices=2 → OK
   → INSERT (iPhone, last_seen=NOW())
   → вернуть vless://…
active_connections:  [ (iPhone, 12:00) ]        (1 запись)
```

### ✅ Юзер подключает 2-е устройство
```
active_connections:  [ (iPhone, 12:00) ]        (1 активная)
POST /api/v1/vpn/devices?name=PC
   → COUNT активных = 1 < 2 → OK
   → INSERT (PC, last_seen=NOW())
active_connections:  [ (iPhone, 12:00), (PC, 12:05) ]
```

### ❌ Юзер пытается подключить 3-е
```
active_connections:  [ (iPhone, 12:03), (PC, 12:05) ]   (обе активны)
POST /api/v1/vpn/devices?name=Tablet
   → COUNT активных = 2 >= 2 → REJECT
   → 403 "device limit exceeded: 2/2 devices active"
```

### ⏱ Юзер выключил iPhone на час — слот автоматически освободился
```
Т0 (12:00):  active = [ (iPhone, 12:00), (PC, 12:00) ]    (2 активных)
             iPhone выключили (не гонит трафик)

Т+5 мин:     heartbeat видит — трафик юзера стоит →
             last_seen у обоих остался 12:00

Т+6 мин:     COUNT активных WHERE last_seen > (12:06 - 5min = 12:01) = 0
             (12:00 < 12:01 → оба считаются "мёртвыми")

   Но! если PC сейчас гонит трафик, heartbeat обновит last_seen = NOW() для
   ОБОИХ строк (потому что Xray даёт один счётчик на весь UUID). Это
   ограничение текущей модели — см. ниже.

Т+7 мин:  POST /api/v1/vpn/devices?name=Tablet
             → COUNT активных = 0 < 2 → OK, вернуть ссылку
active = [ (iPhone,12:00),(PC,12:00),(Tablet,12:07) ]
```

### ✂️ Юзер ручками отключил PC через UI
```
DELETE /api/v1/vpn/devices/11   (connection_id=11, PC)
   → DELETE FROM active_connections WHERE id = 11
active_connections:  [ (iPhone, 12:03) ]        (слот сразу свободен)
```

---

## ⚠️ Честные ограничения нашей модели

### 1. Мы не можем определить «какое именно устройство гонит трафик»
У юзера один UUID → Xray даёт общий счётчик на весь UUID. Когда heartbeat видит рост трафика, он не знает какое из 2 устройств активно.

**Следствие:** `UPDATE last_seen = NOW() WHERE vpn_user_id = X` обновляет **все** устройства юзера сразу. То есть если хоть одно активно, остальные **тоже** считаются активными. Слот освободится только когда **все** устройства юзера молчат 5 минут.

**Почему это ок для MVP:** юзеру всё равно — он получил 2 слота, использует их как хочет. А злоупотребление (раздать UUID друзьям) ограничено тем, что это **его** трафик и **его** IP от Xray.

### 2. Лимит на выдаче ссылки, не на подключении
Если юзер уже получил 2 ссылки, никто не мешает ему импортировать одну и ту же на iPhone, iPad, PC, ноутбук жены... Xray пропустит всех — один UUID.

**Hard-limit (будущее):** отдельный UUID на каждое устройство. Тогда Xray Stats разделит трафик по email (`user5-iphone@vpn.local`, `user5-pc@vpn.local`), и физический отзыв ненужного устройства — это `xray.RemoveUser(email)`. Это переделка модели `vpn_users` — отложили.

### 3. Rate-limit на проверку лимита
Если юзер нажимает "Добавить устройство" 100 раз в секунду — у нас 100 SELECT'ов. В MVP не проблема, но на рост надо будет добавить кэш или advisory-lock.

---

## 🗺️ Что конкретно появится в коде

| Файл | Что поменяется |
|---|---|
| `services/vpn-service/internal/service/vpn.go` | Новый метод `RequestVLESSLink(userID, serverID, deviceID)` — проверка лимита + INSERT active_connection + вызов `GenerateVLESSLink` |
| Новая горутина в `services/vpn-service/internal/app/app.go` | `startHeartbeatLoop(60s)` — тикер, опрос Xray Stats, UPDATE last_seen |
| Proto `shared/proto/vpn/v1/vpn.proto` | `GetVLESSLinkRequest` + `device_identifier` (опционально, при выдаче слота занимается) |
| Repo `services/vpn-service/internal/repository/vpn.go` | `CountActiveConnections(userID, sinceWindow)`, `UpsertActiveConnection(...)`, `UpdateLastSeenForUser(...)`, `GetMaxDevices(vpnUserID)` |
| Gateway `services/gateway/internal/handler/vpn.go` | HTTP-ручка `GET /api/v1/vpn/link` (с `device_id` query-param) + `DELETE /api/v1/vpn/devices/:id` |

Планируемое время: **0.5 дня**.

---

## 🎯 Итог в одной фразе

> **Лимит устройств = счётчик «живых» записей в таблице `active_connections`.**  
> Запись добавляется при выдаче ссылки. Запись «живёт» пока Xray показывает рост трафика (heartbeat каждую минуту). Если 5 минут тихо — запись считается мёртвой, слот освобождается. Ручное отключение через UI = `DELETE` записи.

---

## 📚 См. также

- [xray-integration.md](./xray-integration.md) — как VPN Service общается с Xray
- [01-mvp-plan.md](../tasks/01-mvp-plan.md) — общий план MVP
- [02-mvp-c-implementation.md](../tasks/02-mvp-c-implementation.md) — прогресс по этапам
