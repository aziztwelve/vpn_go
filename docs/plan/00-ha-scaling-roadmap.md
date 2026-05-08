# 00. Roadmap: HA + масштабирование до 10 000+ юзеров

**Дата:** 2026-05-03
**Статус:** 📝 Черновик — план без реализации
**Автор:** Aziz + Devin
**Связанные:**
- [`tasks/08-ha-backend-mirror.md`](../tasks/08-ha-backend-mirror.md) — старый план HA (был отложен, теперь активируем)
- [`ARCHITECTURE.md`](../ARCHITECTURE.md) — текущая архитектура (single backend)
- [`services/multi-server.md`](../services/multi-server.md) — multi-server Xray (уже работает)

---

## 🎯 Цель

Подготовить систему к росту до 10 000+ пользователей с минимальным риском:

- **Доступность:** 99.5%+ uptime (≤3.5 часов простоя в месяц)
- **HA backend:** при падении основного VPS — автоматическое переключение на зеркало за < 5 минут
- **Масштабирование:** возможность горизонтально добавлять Xray exit-ноды без рестарта backend'а (это уже работает — см. multi-server.md)
- **Без k8s:** использовать docker-compose + active-passive replication. K8s не вводить, пока не появится команда из 5+ разработчиков и >20 микросервисов

---

## 📌 Контекст и вводные

### Текущее состояние (на момент написания)
- 101 пользователь зарегистрирован, 3 платящих, 6 активных подписок
- 1 backend VPS (Hetzner Falkenstein, `178.104.217.201`) — single point of failure
- 2 Xray exit-ноды (DE + FI/NL) + локальный Xray на backend
- Postgres self-hosted в docker (`vpn-postgres`)
- Все сервисы (Caddy, Gateway, vpn-core, payment, auth, sub, vpn-next) — на одном VPS
- Telegram bot webhook привязан к `cdn.osmonai.com/api/v1/telegram/webhook`
- DNS: см. факты ниже (заполнить)

### Что уже сделано
- ✅ Multi-server Xray (горизонтально добавляемые exit-ноды через `ResyncServer`)
- ✅ Клиентский Xray-балансер «АВТО ВЫБОР» (burstObservatory + leastLoad)
- ✅ Subscription endpoint (`/api/v1/subscription/{token}`) — самодостаточный, не требует backend для уже подключённых юзеров

### Что НЕ сделано (критические дыры)
- ❌ Бэкапы Postgres (заполнить факты ниже)
- ❌ Hot replica
- ❌ Failover на уровне DNS / LB
- ❌ Мониторинг (Prometheus/Grafana/alerts)
- ❌ Централизованные логи
- ❌ Rate limiting Gateway

---

## 🧱 Разделение типов отказов

Это важно — разные отказы решаются разными механизмами, иначе план путается.

| Что упало | Что ломается | Решение | Где это в roadmap |
|---|---|---|---|
| **Exit-нода Xray** (Germany, Finland, …) | VPN-туннель юзера на этой ноде не работает | Уже решено: multi-server + «АВТО ВЫБОР» с `leastLoad` балансером на клиенте | ✅ Done |
| **Backend** (API, Mini App, бот, БД) | Новые подписки, Mini App, бот не работают. Уже подключённый VPN продолжает работать (трафик идёт юзер → exit-нода напрямую) | HA backend: replica + DNS failover / LB | Уровень 1 |
| **Датацентр** (весь Hetzner Falkenstein) | Всё, кроме exit-нод в других ДЦ | Geo-распределённое зеркало (другой ДЦ или провайдер) | Уровень 1 |
| **Подсеть провайдера** (РКН блочит Hetzner) | Mini App, бот — недоступны через РФ-провайдеров | Зеркало в другой стране/у другого провайдера + Cloudflare proxy | Уровень 2 |
| **Telegram заблокирован в РФ** | Mini App недоступен (требует TG-клиент), backend работает | Решается на стороне юзера (его VPN), на нашей стороне ничего не сделать | — |

---

## 🚫 Что НЕ делать

### Kubernetes — НЕ внедрять

Объективные причины:

1. **k8s сам по себе не даёт HA.** Нужен 3-node etcd кластер (control-plane), иначе он сам — SPOF. Это +3 VPS только под управление кластером.
2. **k8s не реплицирует Postgres.** StatefulSet с Postgres — отдельная сложная задача (Zalando operator / CloudNativePG / Patroni). Все managed Postgres-as-a-service всё равно живут вне k8s-кластера.
3. **k8s не решает «РКН заблочил наш IP».** Это решается на DNS/CDN-уровне, k8s к этому не относится.
4. **k8s ломается чаще, чем docker-compose.** Время будет уходить на «pod в CrashLoopBackOff», «куда делся PVC», «CNI plugin not ready» вместо разработки фичей.
5. **Минимальная стоимость k8s-кластера** (3 control-plane + 2 worker + managed БД) ≈ €100/мес против €30 на текущем self-hosted сетапе.

K8s имеет смысл **только** при:
- Команда 5+ разработчиков
- 20+ микросервисов
- Multi-region active-active с автоскейлом
- Канареечные / blue-green деплои с десятками версий одновременно

При 10к юзеров и одном-двух разработчиках — это минус несколько недель жизни на администрирование, пользы 0.

### Активный multi-master Postgres — НЕ внедрять

Active-active с записью в обе БД — это путь в split-brain и конфликты записи. Стандарт индустрии для нашего масштаба — **active-passive с failover**. Запись всегда в одну БД, реплика только читает или ждёт promote.

### Managed Postgres ($50+/мес) — пока НЕ платить

Self-hosted Postgres с replica + бэкапами справляется до 100к юзеров. Managed (Yandex/Selectel/RDS) подключить, когда упрёшься в DBA-задачи (point-in-time recovery, тонкий тюнинг, шардирование). Сейчас не нужно.

---

## 🛣 Roadmap: уровни приоритета

### 🔴 Уровень 0 — обязательное прямо сейчас (до любого HA)

Без этого любой HA-сетап бессмысленен. Это база.

| # | Задача | Время | Стоимость | Зачем |
|---|---|---|---|---|
| 0.1 | **Cold-бэкап Postgres** — `pg_dump` cron раз в час → S3 (Selectel/B2/Cloudflare R2) | 2-3ч | ~$1/мес | Без бэкапа любая авария БД = «сервис заново с нуля» |
| 0.2 | **Мониторинг** — Prometheus + Grafana + alerting в Telegram | 1 день | $0 (self-hosted) | Узнавать о падении не от юзеров |
| 0.3 | **Централизованные логи** — Loki + Promtail или Vector → файл/S3 | 4-6ч | $0 | Разбираться задним числом, что произошло |
| 0.4 | **Rate limiting Gateway** — `chi httprate` на публичные ручки | 2-3ч | $0 | Защита от единичных скриптов и DoS |
| 0.5 | **Cloudflare proxy перед `cdn.osmonai.com`** — оранжевое облако | 30 мин | $0 (Free) | DDoS-защита, скрытие IP backend'а, кэш статики Mini App |
| 0.6 | **Health-check endpoint** `/health` — реальный (Postgres, gRPC, Xray pool), не просто `200 OK` | 1-2ч | $0 | Без этого failover-логика не работает |

**Триггер:** делать **до** того, как количество платящих превысит 50 (≈сейчас).

---

### 🟡 Уровень 1 — HA backend (нужно при первой 1000 юзеров или 100 платящих)

| # | Задача | Время | Стоимость | Зачем |
|---|---|---|---|---|
| 1.1 | **Hot Postgres replica** — streaming replication на второй VPS в другом ДЦ (Helsinki / SPb / Frankfurt) | 4-6ч | +€5–10/мес VPS | RPO < 30с, готовая к promote БД |
| 1.2 | **Stateless Gateway** — никаких локальных файлов, всё в БД/S3 | 2-3ч | $0 | Условие для multi-instance |
| 1.3 | **Leader election для cron-задач** — advisory lock в Postgres | 2-3ч | $0 | Чтобы при двух работающих backend'ах cron не запустился дважды |
| 1.4 | **Idempotent Telegram webhook handler** | 1-2ч | $0 | Один update может прийти дважды во время failover'а |
| 1.5 | **DNS на Cloudflare** (перевести NS), TTL 60с для A-записей | 1ч + ожидание propagation | $0 | Для быстрого DNS failover'а |
| 1.6 | **Скрипт `failover.sh`** — promote replica + смена DNS A + setWebhook + включение cron на standby | 3-4ч | $0 | Ручной + автоматический failover |
| 1.7 | **Health-check daemon** (UptimeRobot или свой 50-строчник на 3-й машине) | 2-3ч | $0–10/мес | Триггер для автоматического failover |
| 1.8 | **Mini App fetchWithFailover** — JS пробует `api.osmonai.com`, потом `api2.osmonai.com` | 2ч | $0 | Юзеры со старым DNS-кэшем не теряют доступ |

**Триггер:** делать когда платящих ~100+ или произошёл первый инцидент простоя > 10 минут.

**Целевые метрики:**
- RTO: < 5 минут
- RPO: < 30 секунд
- Uptime: 99.5%+

---

### 🟢 Уровень 2 — масштабирование (нужно при 5–10к юзеров)

| # | Задача | Время | Стоимость | Зачем |
|---|---|---|---|---|
| 2.1 | **PgBouncer перед Postgres** — пулер соединений (transaction mode) | 3-4ч | $0 | Без него при 200+ конкурентных запросах Postgres упрётся в `max_connections` |
| 2.2 | **Read replica для тяжёлых SELECT'ов** — отдельная реплика, разгружает primary | 2-3ч (после 1.1) | $0 (та же replica VPS) | Списки серверов, статистика, аналитика — всё через read replica |
| 2.3 | **Очередь для платёжных webhook'ов** — Redis/PG queue + idempotent worker | 1-2 дня | $0 | Wata/Platega делают retry, потеря webhook'а = потерянная подписка |
| 2.4 | **Очередь для TG-рассылок** — TG режет на 30 msg/sec на бота | 4-6ч | $0 | Массовые рассылки (нотификации, апсейлы) |
| 2.5 | **+5 Xray exit-нод** (DE-02, NL-02, FI-02, JP-01, TR-01) | 1ч на ноду | +€20–25/мес | На 10к одновременных туннелей нужно 5–10 нод по 1 Gbit/s |
| 2.6 | **Cloudflare Load Balancer** (опц.) — заменяет DNS-failover, переключение за секунды | 2-3ч | +$5/мес | Если RTO < 1 минута становится критичным |
| 2.7 | **Connection pooling в Go-сервисах** — `pgxpool` с правильными лимитами | 2ч | $0 | Сейчас, скорее всего, дефолтные настройки |
| 2.8 | **Backup тестирование** — еженедельный restore из S3 в test-инстанс | 2-3ч | $0 | Без проверки бэкапы могут оказаться битыми |

**Триггер:** делать когда зарегистрированных >5000 или платящих >500.

---

### 🔵 Уровень 3 — оптимизация и масштаб (после 10к, не раньше)

| # | Задача | Время | Стоимость | Зачем |
|---|---|---|---|---|
| 3.1 | **CDN для Mini App статики** (Cloudflare/BunnyCDN) | 2-3ч | $0–5/мес | Если статика разрастётся (изображения, видео) |
| 3.2 | **Партиционирование `payments` и `active_connections`** по `created_at` (Postgres native partitioning) | 1 день | $0 | Когда таблицы перевалят за 10М строк |
| 3.3 | **Geo-распределённые backend'ы** (RU + EU active-active с CRDT/eventual consistency) | 1-2 недели | +€50/мес | Если упрёшься в latency или РКН |
| 3.4 | **Managed Postgres** (Yandex Cloud / Selectel Managed PG) | 1 день | $30–80/мес | Если self-hosted станет узким местом по DBA-задачам |
| 3.5 | **Шардирование БД по `user_id`** | 2-4 недели | $0–50/мес | Когда одна БД упрётся в IOPS/CPU (вряд ли при 10к, скорее при 100к+) |

**Триггер:** делать **только** при конкретных метрических порогах (latency, IOPS, ошибки), не «на всякий случай».

---

## 🤖 Telegram bot — про «два домена»

**Важное ограничение Telegram, чтобы не было иллюзий:**

- **Webhook URL у бота — РОВНО ОДИН.** `setWebhook` переписывает предыдущий. Нельзя сказать TG «если первый URL не отвечает — стучи во второй».
- Значит, **«два домена для бота» — это миф**. Для бота нужно либо:
  - Один домен с DNS/LB failover на разные IP (то, что в Уровне 1 делаем)
  - При failover'е вызывать `setWebhook` на новый URL (занимает секунды, но это последовательное переключение, не параллельная работа)
- **Mini App** — другая история. Там можно `fetchWithFailover` пробовать несколько endpoint'ов. Но для бота это не даст ничего.

---

## 🏗 Целевая архитектура на 10к юзеров

```
┌──────────────────────────────────────────────────────────────┐
│  Cloudflare (DNS + proxy + опц. LB)                          │
│  - DDoS protection                                           │
│  - SSL termination                                           │
│  - Health-check active-passive failover                      │
│  - Кэш статики Mini App                                      │
└────────────────────┬─────────────────────────────────────────┘
                     │
        ┌────────────┴────────────┐
        ▼                         ▼
┌──────────────────┐      ┌──────────────────┐
│ Backend Primary  │      │ Backend Standby  │
│ Hetzner Helsinki │      │ Hetzner Frankfurt│
│ CCX23 8 vCPU     │      │ CCX23 8 vCPU     │
│                  │      │                  │
│ Caddy            │      │ Caddy            │
│ Gateway × 2      │      │ Gateway × 2      │
│ vpn-core         │      │ vpn-core (read)  │
│ payment-svc      │      │ payment-svc      │
│ auth/sub-svc     │      │ auth/sub-svc     │
│ PgBouncer        │      │ PgBouncer        │
│ Postgres MASTER ─┼─────►│ Postgres REPLICA │
│ Redis (queue)    │      │ Redis (replica)  │
│ Prometheus       │      │ Prometheus       │
└──────────────────┘      └──────────────────┘
        │                         │
        └────────────┬────────────┘
                     │ (cold backups, hourly)
                     ▼
            ┌─────────────────┐
            │ S3 (Selectel)   │
            │ pg_dump archive │
            │ Logs archive    │
            └─────────────────┘

   Xray exit-ноды (отдельно от backend'ов, географически распределены):
   ┌────────┐ ┌────────┐ ┌────────┐ ┌────────┐ ┌────────┐
   │ DE-01  │ │ NL-01  │ │ FI-01  │ │ JP-01  │ │ TR-01  │
   └────────┘ └────────┘ └────────┘ └────────┘ └────────┘
   (управляются через vpn_servers + ResyncServer, см. multi-server.md)
```

---

## 💰 Стоимость по уровням

| Уровень | Дополнительная стоимость | Накопительная |
|---|---|---|
| Сейчас (1 VPS, без HA) | €30/мес | €30/мес |
| + Уровень 0 | + $1/мес (S3) | €30/мес + $1 |
| + Уровень 1 (HA) | + €5–10/мес (replica VPS) | €40/мес |
| + Уровень 2 (5 exit-нод, queue, etc.) | + €25–30/мес | €70/мес |
| + Уровень 3 (CDN, опц. managed PG) | + €30–80/мес | €100–150/мес |

**Прогноз при 10к юзеров:**
- Конверсия 5% → 500 платящих
- Средний чек 200₽ → 100 000₽/мес выручка
- Инфра 10 000₽/мес = **10% выручки** (здоровое соотношение)

---

## 📊 Метрики для триггеров перехода между уровнями

| Метрика | Уровень 1 | Уровень 2 | Уровень 3 |
|---|---|---|---|
| Зарегистрированных юзеров | 1 000 | 5 000 | 50 000 |
| Платящих | 100 | 500 | 5 000 |
| Активных VPN-туннелей | 200 | 2 000 | 20 000 |
| Postgres CPU (avg) | — | > 30% | > 60% |
| Postgres размер БД | — | > 5 GB | > 50 GB |
| Backend RPS (peak) | — | > 50 | > 500 |
| Простой за месяц | > 10 мин | > 30 мин | > 1ч |

---

## 🚦 Текущий чек-лист на ближайшие 2 недели

Приоритеты пересчитаны после аудита ([`01-current-state-audit.md`](./01-current-state-audit.md)). В порядке убывания критичности:

### 🔥 Срочно (сегодня-завтра)
- [x] **0.1** Hourly `pg_dump` cron + rsync на `nl01` (вместо S3 — бесплатно, гео-распределённо). См. [`02-postgres-backup.md`](./02-postgres-backup.md). ✅ 2026-05-03
- [ ] **VPS-1** Создать swap-файл 4 GB (`fallocate -l 4G /swapfile && mkswap /swapfile && swapon /swapfile`). Сейчас swap = 0, при OOM рискуем потерять контейнеры.
- [x] **0.6** Real `/live`, `/ready`, `/health` в Gateway с пингом БД через gRPC Health v1. См. [`03-health-checks.md`](./03-health-checks.md). ✅ 2026-05-03

### 📅 На этой неделе
- [ ] **1.5** Перевести NS `osmonai.com` на Cloudflare (сейчас Namecheap, CF proxy не подключён).
- [ ] **0.5** Включить CF proxy перед `cdn.osmonai.com` и `api.osmonai.com` (Free tier, бесплатно).
- [ ] **1.1** Поднять hot Postgres replica на втором VPS (Hetzner Helsinki или Selectel SPb для гео-независимости).
- [ ] **0.2** Prometheus + Grafana + Telegram alerts (метрики + базовый alerting).
- [ ] **0.4** Rate limiting Gateway (`chi httprate`).

### 📅 На следующей неделе
- [ ] **0.3** Loki + Promtail (или Vector) для централизованных логов.
- [ ] **1.2** Аудит Gateway на stateless-чистоту (никаких локальных файлов).
- [ ] **1.4** Аудит Telegram webhook handler на идемпотентность.
- [ ] **1.6** Скрипт `failover.sh` (promote replica + смена DNS A через CF API + setWebhook + cron включить).
- [ ] **1.7** Health-check daemon (UptimeRobot или свой) с триггером failover'а.
- [ ] **1.8** `fetchWithFailover` в Mini App.

---

## 🧾 Факты текущего состояния

Снято с прода 2026-05-03. Полностью — в [`01-current-state-audit.md`](./01-current-state-audit.md). Краткая выжимка:

| Слой | Состояние |
|---|---|
| Бэкапы Postgres | ❌ Нет cron / systemd timer. Один ручной dump от 27.04. `archive_mode = off`. |
| Реплика Postgres | ❌ Нет (`pg_stat_replication` пуст). `wal_level = replica` готов. |
| DNS-провайдер | Namecheap (`registrar-servers.com`). CF не подключён. |
| CF proxy | ❌ Не подключён. IP backend'а торчит наружу. |
| Размер БД | 9.4 MB, 64 MB на диске. Самые большие таблицы — vpn_users/payments/users (по 100–150 KB). |
| Postgres connections | 15 / 100 (15%). PgBouncer пока не нужен. |
| VPS | 4 CPU / 8 GB RAM / 150 GB disk. Load avg 0.5, CPU idle 89%, swap = 0. Запас железа огромный. |
| Trafic backend | Vpn-xray локальный: 82 GB/84 GB за 5 дней. Backend-сервисы — единицы MB. |
| Мониторинг / алерты | ❌ Нет. |
| Health-check / readiness | ❌ Нет реального endpoint'а. |
| Multi-server Xray | ✅ Работает (DE, NL, FI). |
| Subscription endpoint самодостаточный | ✅ Работает (даунтайм backend'а не ломает уже подключённых). |

**Главный вывод:** железо текущего VPS дотянет до 10к юзеров без апгрейда. Узкое место — **отсутствие резервирования** (бэкапы, replica, CF, мониторинг). Эту дыру и закрываем планом.

---

## 🔗 Связанные документы

- [`tasks/08-ha-backend-mirror.md`](../tasks/08-ha-backend-mirror.md) — детали HA backend mirror (старый отложенный таск)
- [`tasks/07-fake-cdn-domain.md`](../tasks/07-fake-cdn-domain.md) — fake CDN домен (предусловие 08-го)
- [`services/multi-server.md`](../services/multi-server.md) — multi-server Xray (уже работает)
- [`SUBSCRIPTION.md`](../SUBSCRIPTION.md) — клиентский Xray-балансер «АВТО ВЫБОР»
- [`ARCHITECTURE.md`](../ARCHITECTURE.md) — текущая архитектура

---

## 📝 Принципы (зачем именно так)

1. **Инкрементально, не «всё сразу».** Каждый уровень — самостоятельно ценный. Не нужно делать Уровень 2, чтобы получить пользу от Уровня 0.
2. **Простое решение в первую очередь.** DNS-failover проще, чем anycast. Anycast проще, чем k8s. K8s сложнее всего и нужен в последнюю очередь.
3. **Каждое решение должно иметь обоснование в метриках.** «Делаем потому что у других так» — не аргумент. Должна быть конкретная цифра, которая упрётся.
4. **Бэкап важнее HA.** Без бэкапа любой HA — иллюзия. С бэкапом без HA можно жить.
5. **Stateless backend > stateful.** Чем меньше состояния на backend-машине, тем легче её копировать. Всё состояние — в Postgres + S3.
6. **Не привязываться к одному провайдеру там, где это критично.** Backend и replica — в разных ДЦ или у разных провайдеров (Hetzner + Selectel, например).
