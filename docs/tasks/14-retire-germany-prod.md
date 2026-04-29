# 14. Снять Germany (Hetzner) с prod-роутинга — оставить как dev/code-сервер

**Дата:** 2026-04-29
**Статус:** 🟡 Обсуждение
**Автор:** Devin + aziz
**Источник:** [habr.com/ru/articles/1021160](https://habr.com/ru/articles/1021160/) — рекомендация «Не используйте Hetzner/OVH/DigitalOcean — их подсети массово заблокированы»

---

## 🎯 Цель

Перестать выдавать `Germany Server` (`178.104.217.201:8443`) живым
prod-юзерам в subscription. Оставить машину в инвентаре только как
**бэкенд / dev / code-сервер**:
- Postgres + vpn-core + vpn-service + gateway + Telegram bot + Mini App
  продолжают жить там же.
- Локальный Xray-контейнер (`vpn-xray` на `:8443`) — оставляем для
  ручного тестирования / разработки.
- Из subscription-выдачи Germany **пропадает**.

---

## 📚 Контекст

### Что там сейчас на Hetzner Falkenstein

Один VPS `178.104.217.201` (Hetzner DE) совмещает две роли:

1. **Бэкенд приложения:** Postgres, vpn-service (gRPC), gateway (HTTP),
   Telegram bot, Mini App + Caddy для TLS на `:443` (домен
   `cli.cloud-cdn.click` или аналог).
2. **VPN-узел:** контейнер `vpn-xray` на `:8443` с Reality, который
   реально принимает клиентский трафик и отдаёт его в интернет.

### Проблемы Germany как prod VPN-узла

1. **Hetzner — очень засвеченный AS.** Подсети Hetzner Falkenstein
   массово заблокированы ТСПУ для VPN/прокси-трафика. Это уже даёт
   юзерам performance issues («медленно с мобильного»).
2. **Порт `:8443` — DPI-аномалия.** Настоящий HTTPS живёт на `:443`.
   TLS-handshake на других портах для DPI — сильный сигнал «это VPN /
   прокси». Перенос на `:443` невозможен — там Caddy для Mini App.
3. **Один IP = single point of disclosure.** Если Germany IP попадёт
   в РКН-список, отвалится **и backend** (subscription-API, бот) — а
   не только Xray-узел. Это критично: отключение бэкенда = смерть
   всего сервиса.

### Альтернативы и выбор

В <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/docs/tasks/README.md" />
рассматривался вариант «вынести Xray на отдельный VPS, оставить
бэкенд». Он ОК, но это +1 VPS и +1 deploy-pipeline.

Сейчас у нас уже есть:
- `Finland-01` — отдельный VPS (vdsina или Aeza)
- `Netherlands-01` — отдельный VPS

Этого достаточно для prod: две географии вне Hetzner-AS, обе на `:443`.
Снимать Germany — самый дешёвый ход.

---

## 🏗 Решение

### Шаг 1. `is_active = false` для Germany

```sql
UPDATE vpn_servers
SET is_active = false,
    description = COALESCE(description, '') || ' [retired prod 2026-04-29: dev/code only]'
WHERE host = '178.104.217.201';
```

Сервер остаётся в БД (не DELETE!) чтобы:
- ResyncServer + add_server-скрипты на нём ещё работали для тестов.
- Логи / telemetry-история сохранилась.
- Можно быстро вернуть `is_active = true`, если что-то сгорит.

### Шаг 2. Subscription-handler фильтрует по `is_active`

Проверить что в <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/services/gateway/internal/handler/subscription_config.go" />
выбор серверов идёт через `WHERE is_active = true` (или эквивалентный
фильтр в `vpn-service.ListServers`). Если фильтр уже есть — ничего
менять не надо. Если нет — добавить.

Также убрать из захардкоженных режимов раздела `🇩🇪 Germany` — должен
остаться только динамический список из БД (Finland / Netherlands и
далее).

### Шаг 3. Существующие юзеры

Юзеры, у которых сейчас в подписке Germany как «лучший сервер»:
- При следующем рефреше subscription (TTL ~ Profile-Update-Interval)
  получат Finland или Netherlands вместо Germany.
- Активные коннекты не разрываются принудительно — отвалятся когда
  клиент сам переподключится.

Делать в окно low-traffic (3–6 утра МСК).

### Шаг 4. Документация

- В <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/docs/SUBSCRIPTION.md" />:
  убрать примеры `vless://...@178.104.217.201:8443/...`, заменить на
  Finland/Netherlands.
- В <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/docs/services/multi-server.md" />:
  обновить список prod-серверов.
- В <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/add_server/README.md" />:
  явно прописать «Germany — code/dev only, не для prod».

### Шаг 5. Локальный Xray остаётся

`docker compose up -d vpn-xray` на Germany-машине **продолжает работать**
для:
- E2E-тестов из dev-среды.
- Ручной отладки subscription / Xray API.
- Резерва на случай экстренного возврата (просто `is_active=true`).

Никаких правок в `deploy/compose/xray/config.json` не требуется.

---

## ⚠️ Риски

1. **Юзеры с старыми ссылками.** Те, у кого в клиенте сохранён
   именно Germany-вариант (профили `🇩🇪 Germany` хардкодом из
   subscription_config.go), продолжат бить в `:8443`. После шага 1
   Xray-API на Germany ещё работает — никаких 0 пользователей не
   будет. Но при следующем рефреше — они исчезнут.
2. **SLA на бэкенд возрос.** Раньше при падении Germany рушилось всё
   (бэкенд + Xray на нём же). Теперь падает «всё кроме prod-VPN
   трафика» — клиенты Finland/Netherlands продолжат работать, но
   subscription / бот / оплата будут лежать. Это **не хуже**, чем
   было, но **тоже плохо**. Долгосрочно — задача 8 (HA backend
   mirror).
3. **Если оставить Germany полу-живым (Xray работает, но не выдаётся
   юзерам)** — он съедает RAM/CPU на бэкенд-машине. На Hetzner CX21
   это ~200 МБ RAM, не критично. Можно ребут только Xray-контейнера
   убрать через `docker compose stop vpn-xray`, если ресурсы нужнее
   бэкенду.

---

## ✅ Проверка

```bash
# 1. БД: Germany выпала из активных
docker exec vpn-postgres psql -U vpn -d vpn -c \
  "SELECT id, name, host, port, is_active FROM vpn_servers ORDER BY id;"
# Germany должен быть is_active=false

# 2. Subscription для тест-юзера не содержит 178.104.217.201
curl -s "https://<gw>/sub/<token>" | base64 -d | grep -c '178.104.217.201'
# должно быть 0

# 3. Активные клиенты Germany постепенно мигрируют
docker exec vpn-postgres psql -U vpn -d vpn -c \
  "SELECT server_id, COUNT(*) FROM active_connections
   WHERE last_seen > NOW() - INTERVAL '5 min'
   GROUP BY server_id;"
# Germany count → 0 за 1-2 часа
```

---

## 📦 Объём работ

| Шаг | Время |
|---|---|
| SQL `UPDATE is_active = false` | 5 мин |
| Проверка `subscription_config.go` фильтра | 30 мин |
| Удаление хардкодных Germany-режимов (если есть) | 30 мин |
| Обновление SUBSCRIPTION.md / multi-server.md / add_server/README.md | 30 мин |
| Smoke-тест с 2-3 юзеров | 30 мин |

Итого: **~2 часа**.

---

## 🗺 Связанные файлы

- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/services/gateway/internal/handler/subscription_config.go" />
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/services/gateway/internal/handler/subscription_config_test.go" />
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/docs/SUBSCRIPTION.md" />
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/docs/services/multi-server.md" />
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/add_server/README.md" />

---

## ⛓ Связанные задачи

- Task 8 (HA backend mirror) — долгосрочное решение для SPOF бэкенда
  на Germany.
- Task 11/12/13 — применяются к Finland + Netherlands; Germany можно
  не трогать (dev-only).
