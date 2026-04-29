# 12. SNI rotation: 4 `serverNames` per inbound

**Дата:** 2026-04-29
**Статус:** 🟡 Обсуждение
**Автор:** Devin + aziz
**Источник:** [habr.com/ru/articles/1021160](https://habr.com/ru/articles/1021160/) — UPD2 от Sergei-thinker

---

## 🎯 Цель

На каждом Xray-инстансе держать **4 разных donor SNI** в `serverNames`
вместо одного, чтобы ломать DPI fingerprint «IP X всегда отдаёт SNI Y».

При выдаче VLESS-ссылки конкретному юзеру в `?sni=` подставлять
**случайный из четырёх**, чтобы у разных юзеров на одном сервере были
разные SNI в TLS handshake.

---

## 📚 Контекст

Сейчас:
- В `vpn_servers.server_names` хранится одна строка (`'apple.com'`).
- В Xray `config.json` `serverNames: ["apple.com"]` — массив из одного.
- В VLESS-ссылке `?sni=apple.com` — фиксированно для всех юзеров на этом сервере.

Это создаёт паттерн который ТСПУ ловит на больших объёмах:
> «На IP `146.103.112.91` 200 разных source IP за час, и **все** делают
> TLS handshake с SNI `apple.com`. Это аномалия — у нормального CDN-узла
> Apple бы был один-два SNI per IP, а не строго один на тысячи коннектов».

Автор Habr-статьи в UPD2 пишет: «по 4 serverNames на каждый Reality
inbound вместо одного, паттерн "IP ↔ один SNI" для DPI ломается».

---

## 🏗 Решение

### 1. Schema migration: `server_names` → JSONB array

**Сейчас (миграция 001):**
```sql
server_names TEXT NOT NULL DEFAULT 'github.com'
```

**Стало (новая миграция 006 — 005 уже занята `add_subscription_token`):**
```sql
ALTER TABLE vpn_servers
  ALTER COLUMN server_names TYPE JSONB
  USING (CASE
    WHEN server_names::text ~ '^\[.*\]$'
      THEN server_names::jsonb
    ELSE jsonb_build_array(server_names::text)
  END),
  ALTER COLUMN server_names SET DEFAULT '["apple.com"]'::jsonb;

-- Backfill: для каждого сервера с одной строкой — раздуть до 4 SNI.
-- Логика выбора 4 SNI per server — отдельная задача 13 (RealiTLScanner).
-- На этапе миграции просто оставить как есть (1 SNI), а task 13
-- заполнит actual data per server.
```

**ВАЖНО:** существующая schema.sql (deploy/schema.sql) **врёт** — там
уже сейчас написано `JSONB NOT NULL DEFAULT '["github.com", ...]'`. Но
в проде — `text`. Этот рассинхрон надо одновременно поправить.

### 2. Model + Repository

`services/vpn-service/internal/model/vpn.go`:
```go
type VPNServer struct {
  // …
  ServerNames []string  // было `string`
}
```

`services/vpn-service/internal/repository/vpn.go` — везде где сейчас
`&server.ServerNames` подменить на сканирование JSONB через
`pq.Array(&server.ServerNames)` или `[]byte` + `json.Unmarshal`.
Везде в `INSERT`/`UPSERT` — `pq.Array(s.ServerNames)`.

### 3. Subscription link generation

`services/vpn-service/internal/service/vpn.go:270`:
```go
// БЫЛО:
params.Add("sni", server.ServerNames)

// СТАЛО:
sni := server.ServerNames[rand.Intn(len(server.ServerNames))]
params.Add("sni", sni)
```

При генерации ссылки для конкретного юзера — берём случайный SNI из
массива. Каждый раз когда юзер рефрешит подписку, может приехать
другой SNI. Это **не ломает** существующее подключение клиента
(VLESS Reality client сохраняет SNI из последней принятой ссылки).

### 4. Xray config

В `deploy-xray-new.sh` менять
```bash
SNI="apple.com"  # дефолт
```
на
```bash
SNI="apple.com,icloud.com,…,…"  # 4 штуки через запятую
```
и в шаблоне `config.json`:
```json
"serverNames": $(printf '%s\n' "$SNI" | jq -Rc 'split(",")')
```

Конкретные 4 SNI per server подбираются в task 13 (RealiTLScanner).

### 5. Применение на live-серверах

Скрипт `add_server/rotate-sni.sh`:
1. Для каждого VPS:
   - Прочитать `config.json`, заменить `serverNames` на новый массив.
   - `docker restart xray` + ResyncServer.
2. `UPDATE vpn_servers SET server_names = $1 WHERE id = $2`.

---

## ⚠️ Риски

1. **Все клиенты с уже выданными ссылками продолжат работать** — у
   них в Reality client сохранён один SNI, который в новом массиве
   тоже есть (apple.com мы оставляем).
2. **Если в `serverNames` есть SNI которого нет в TLS-сертификате,
   который Xray получает от donor'а** — handshake фейлится. SNI
   нужно подбирать ОДНИМ доменом + альтернативными в той же
   подсети (= delegated cert). Task 13 это решает через
   RealiTLScanner.
3. **Migration на проде с активными юзерами** — `ALTER COLUMN ... TYPE JSONB`
   на горячую таблице с 100k строк работает быстро, но **блокирует
   таблицу**. На MVP-объёме (3 строки) — мс.

---

## 📦 Объём работ

| Шаг | Время |
|---|---|
| Миграция 006 (schema rename + USING) | 30 мин |
| `model.VPNServer.ServerNames` `string` → `[]string` | 30 мин |
| `repository/vpn.go`: `pq.Array` в Scan/Insert | 1 час |
| `service/vpn.go`: random pick для subscription | 15 мин |
| Тесты (если будут) + ручная проверка | 1 час |
| `deploy-xray-new.sh` правка | 15 мин |
| `rotate-sni.sh` + применение на 3 VPS | 1 час |

Итого: **0.5 рабочего дня**, без подбора реальных SNI (task 13).

---

## 🗺 Связанные

- Task 13: подбор реальных donor SNI (без него у нас всё равно один
  apple.com и делать ротацию не из чего)
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/services/vpn-service/internal/model/vpn.go" />
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/services/vpn-service/internal/repository/vpn.go" />
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/services/vpn-service/internal/service/vpn.go" />
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/deploy/scripts/deploy-xray-new.sh" />
