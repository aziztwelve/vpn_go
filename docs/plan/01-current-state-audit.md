# 01. Аудит текущего состояния инфраструктуры

**Дата:** 2026-05-03
**Статус:** ✅ Снято с прода
**Сервер:** `178.104.217.201` (Hetzner Falkenstein, `snapshot-380807420-ubuntu-8gb-fsn1-2`)

Этот документ — снимок реального состояния прода, чтобы план [`00-ha-scaling-roadmap.md`](./00-ha-scaling-roadmap.md) опирался на цифры, а не на ощущения.

---

## 🚨 Критические находки (прямо сейчас риск)

| # | Проблема | Риск |
|---|---|---|
| **C-1** | **Бэкапов Postgres практически нет.** Найден один ручной `pg_dump` от 27.04.2026 (`/root/backups/vpn-pg-20260427-195749.sql`, 42 KB). Ни cron, ни systemd timer для автоматических бэкапов нет. Архив WAL отключён (`archive_mode = off`). | Любой сбой БД, удаление таблицы, повреждение FS → теряем 5 дней данных и больше. Восстановления point-in-time нет в принципе. |
| **C-2** | **Реплик Postgres нет.** `pg_stat_replication` пуст. `wal_level = replica` (готов к репликации, но реплик не существует). | RPO бесконечный при потере primary. |
| **C-3** | **CF proxy не подключён.** Домены `cdn.osmonai.com` и `api.osmonai.com` смотрят прямо на IP VPS (`178.104.217.201`). | IP backend'а торчит наружу → DDoS, прямые блокировки IP, нет защиты L7. |
| **C-4** | **Swap = 0.** 8 GB RAM без swap-файла. | При резком всплеске нагрузки → OOM-kill контейнеров. Postgres особенно чувствителен. |
| **C-5** | **Telegram webhook привязан к одному URL** (`cdn.osmonai.com/api/v1/telegram/webhook`). Идемпотентность хендлера не проверена. | При failover'е возможна двойная обработка update'ов. |
| **C-6** | **Health-check endpoint отсутствует** или фиктивный. Нужен реальный `/health` (Postgres ping + gRPC liveness + Xray pool). | Нечем триггерить failover. |

---

## 🌐 DNS

**Регистратор/NS:** Namecheap (`dns1.registrar-servers.com`, `dns2.registrar-servers.com`).

```
osmonai.com.        NS  dns1.registrar-servers.com.
osmonai.com.        NS  dns2.registrar-servers.com.

cdn.osmonai.com.    A   178.104.217.201
api.osmonai.com.    A   178.104.217.201
```

**Что это значит для HA:**
- Cloudflare пока **не подключён** (ни как DNS, ни как proxy).
- API Namecheap для смены A-записей существует ([Namecheap API](https://www.namecheap.com/support/api/intro/)), но грубее CF API. Минимальный TTL Namecheap = 60 сек, на практике пропагация дольше.
- **Рекомендация:** перевести NS на Cloudflare → бесплатный proxy + быстрый DNS API + кэш статики Mini App.

---

## 💾 Postgres

```
Размер БД:                9.4 MB (!) — пока крошечная
Volume на диске:          64 MB (с indexes/WAL)
Connections:              15 / 100 (15%)
Replicas:                 0
wal_level:                replica (готов реплицироваться, но некуда)
archive_mode:             off (нет WAL archiving)
shared_buffers:           128 MB
effective_cache_size:     4 GB
```

### Топ-таблицы
| Таблица | Размер | Строк |
|---|---|---|
| `vpn_users` | 144 KB | 101 |
| `payments` | 128 KB | 15 |
| `users` | 120 KB | 101 |
| `subscriptions` | 104 KB | 101 |
| `bot_starts` | 104 KB | 101 |
| `active_connections` | 104 KB | 67 |
| `referral_links` | 72 KB | 18 |
| `vpn_servers` | 64 KB | 3 |

### Активность
- Последний платёж: 2026-05-02 17:27 UTC
- Последняя регистрация: 2026-05-02 18:33 UTC
- Последний `active_connections.last_seen`: 2026-05-03 00:59 UTC (свежий, юзеры онлайн)

**Вывод:** база сейчас в зачаточном состоянии. До первых проблем с производительностью БД ещё далеко (можно расти на этом сетапе до 100к+ юзеров без шардинга).

---

## 🖥 VPS

```
Hostname:           snapshot-380807420-ubuntu-8gb-fsn1-2
Provider:           Hetzner Falkenstein (Германия)
IP:                 178.104.217.201
CPU:                4 cores
RAM:                7.6 GB
Swap:               0 B  ← добавить swap-файл
Disk:               150 GB (используется 36 GB / 25%)
Uptime:             5 дней 5 часов
Load avg (1/5/15):  0.54 / 0.38 / 0.20
CPU idle:           89%
```

### Контейнеры (docker stats snapshot)

| Контейнер | CPU | Mem | Net I/O (lifetime) |
|---|---|---|---|
| vpn-xray | 0.02% | 51 MB | **82.9 GB / 84.2 GB** |
| vpn-postgres | 0.02% | 68 MB | 45 MB / 131 MB |
| vpn-core | 0.00% | 9 MB | 118 MB / 103 MB |
| vpn-caddy | 0.00% | 41 MB | 108 MB / 113 MB |
| vpn-next | 0.00% | 55 MB | 2.7 MB / 12 MB |
| vpn-gateway | 0.00% | 7 MB | 2.1 MB / 5.7 MB |
| vpn-payment | 0.00% | 7 MB | 1.1 MB / 0.9 MB |
| vpn-subscription | 0.00% | 5 MB | 1.1 MB / 0.9 MB |
| vpn-auth | 0.00% | 5 MB | 0.7 MB / 0.6 MB |
| vpn-referral | 0.00% | 5 MB | 0.4 MB / 0.3 MB |

### Что важно
- **Vpn-xray прокачал 82.9 GB IN / 84.2 GB OUT** за uptime 5 дней. Это локальный Xray на backend'е (Германия в БД, server_id=5). На 10к юзеров с активным трафиком этот сценарий не вытянет — нужны выделенные exit-ноды (что уже частично сделано: NL01, FI02).
- **Backend-сервисы практически не нагружены** (Mem < 10 MB, CPU 0%). Ресурсов с большим запасом.
- **Volume Postgres всего 64 MB.** Можно даже шарить snapshot disk'а Hetzner для cold-бэкапа.

---

## 🛡 Что отсутствует целиком

| Слой | Состояние |
|---|---|
| Автоматические бэкапы БД | ❌ Нет |
| Реплика БД | ❌ Нет |
| Внешнее хранилище бэкапов (S3/B2/R2) | ❌ Нет |
| Cloudflare proxy / DDoS защита | ❌ Нет |
| Мониторинг (Prometheus / Grafana) | ❌ Нет |
| Централизованные логи (Loki/Vector) | ❌ Нет (логи только в `docker logs`) |
| Alerting (Telegram / email при инциденте) | ❌ Нет |
| Rate limiting Gateway | ❌ Нет (или базовый Caddy) |
| Real `/health` endpoint | ⚠️ Не проверено, скорее нет |
| Swap | ❌ Нет |
| HA на Telegram webhook | ❌ Нет (один URL) |
| PgBouncer | ❌ Нет (пока не нужен — 15/100 connections) |

---

## ✅ Что уже хорошо

| Слой | Состояние |
|---|---|
| Multi-server Xray | ✅ Работает (3 сервера: Local DE, NL01, FI02) |
| Клиентский Xray-балансер «АВТО ВЫБОР» | ✅ Работает (burstObservatory + leastLoad) |
| Subscription endpoint самодостаточный | ✅ Работает (юзер с активной подпиской переживёт даунтайм backend'а) |
| Postgres `wal_level = replica` | ✅ Готов к репликации без рестарта |
| Caddy с auto-TLS (LE) | ✅ Сертификаты выпускаются |
| Docker compose на одном FS | ✅ Легко копировать на mirror |
| Telegram bot работает | ✅ Webhook принимает update'ы |
| Низкая нагрузка | ✅ Запас 10x по CPU/RAM до упора |

---

## 🧮 Прогноз ёмкости (на текущем железе)

С нагрузкой backend как сейчас (CPU 0–8%, Mem 2 GB / 8 GB):

| Юзеров | Backend нагрузка | Узкое место |
|---|---|---|
| 1 000 | ~5% CPU, ~3 GB RAM | — |
| 5 000 | ~15% CPU, ~4 GB RAM | TG webhook handler (если рассылки), Postgres connections |
| 10 000 | ~30% CPU, ~5 GB RAM | PgBouncer нужен, swap нужен |
| 50 000 | ~70% CPU, ~7 GB RAM | Read replica, очереди для рассылок, шардирование `payments` |

**На текущем VPS можно дотянуть до 10к юзеров без апгрейда железа.** Узкое место будет не CPU/RAM, а отказоустойчивость (один VPS = SPOF).

Для exit-нод формула другая: **1 Gbit/s ≈ 500–1000 одновременных юзеров** при средней нагрузке. На 10к одновременно подключённых VPN-туннелей нужно **5–10 exit-нод** по 1 Gbit/s.

---

## 🚦 Приоритеты на основе аудита

В порядке убывания критичности:

### Сегодня-завтра (не откладывать)
1. **Hourly `pg_dump` cron + S3** ([Selectel S3](https://selectel.ru/services/cloud/storage/) или Cloudflare R2 — оба от $1/мес). Без этого мы играем в рулетку с данными.
2. **Создать swap-файл 4 GB** (`fallocate /swapfile && swapon`). 5 минут работы, защита от OOM-kill.
3. **Real `/health` endpoint** — проверка Postgres + gRPC + Xray pool.

### На этой неделе
4. **Перевести NS на Cloudflare**, включить proxy на `cdn.osmonai.com`. Бесплатно, мгновенный профит: DDoS-защита + кэш статики + IP скрыт.
5. **Hot Postgres replica** на втором VPS (Hetzner Helsinki или Selectel SPb для географической независимости).
6. **Прометей + Графана + Telegram alerting.** 1 день работы, окупается с первого инцидента.
7. **Rate limiting Gateway** (`chi httprate`).

### На следующей неделе
8. **Автоматический failover-скрипт** + UptimeRobot / свой health-check daemon.
9. **`fetchWithFailover` в Mini App** для двух endpoint'ов.
10. **Аудит идемпотентности Telegram webhook handler**.

### Позже (по триггерам)
11. PgBouncer (когда connections > 50/100)
12. +5 Xray exit-нод (когда одновременных туннелей > 1000)
13. Очередь для платёжных webhook'ов и TG-рассылок (когда платящих > 100)

---

## 🔬 Команды для перепроверки

Эти команды можно запускать раз в неделю, чтобы видеть тренд:

```bash
# Размер БД и connections
docker exec vpn-postgres psql -U vpn -d vpn -c "
  SELECT pg_size_pretty(pg_database_size('vpn')) AS db_size;
  SELECT (SELECT count(*) FROM pg_stat_activity) AS conn,
         (SELECT setting::int FROM pg_settings WHERE name='max_connections') AS max;
  SELECT count(*) replicas FROM pg_stat_replication;
"

# Нагрузка на VPS
uptime; free -h; df -h /; docker stats --no-stream

# Свежесть бэкапа
ls -la /root/backups/ /var/backups/postgres/ 2>/dev/null
# (после внедрения cron смотреть последний файл)

# DNS / CF status
dig +short NS osmonai.com
curl -sI https://cdn.osmonai.com | grep -iE "server|cf-"
```

---

## 🔗 См. также

- [`00-ha-scaling-roadmap.md`](./00-ha-scaling-roadmap.md) — план развития до 10к
- [`tasks/08-ha-backend-mirror.md`](../tasks/08-ha-backend-mirror.md) — детальный план HA mirror
