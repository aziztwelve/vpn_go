# 08. HA Backend: RU primary + foreign mirror (failover при блокировке РКН)

**Дата:** 2026-04-24
**Статус:** ⏸ Отложено — зависит от 07, который тоже отложен. Возвращаемся когда появятся реальные prod-юзеры и нужна High Availability.
**Автор:** Aziz + Devin
**Родительский:** [07-fake-cdn-domain.md](./07-fake-cdn-domain.md)
**Связанные:** [04-caddy-auto-tls.md](./04-caddy-auto-tls.md)

---

## 🎯 Цель

Сделать backend VPN-сервиса устойчивым к блокировке РКН и Telegram-related рискам. Если основной backend в РФ недоступен (из-за блокировки IP, проблем с reg.ru, давления на провайдера) — сервис продолжает работать с зарубежного зеркала. Failover автоматический, простой пользователя — минимальный.

**Метрики цели:**
- RTO (Recovery Time Objective): **< 5 минут** автоматический failover
- RPO (Recovery Point Objective): **< 30 секунд** потери данных при переключении
- Юзер не должен переустанавливать конфиг или терять подписку при failover'е

## 📚 Контекст

В [07-fake-cdn-domain.md](./07-fake-cdn-domain.md) выбрана архитектура с одним backend'ом. Это ОК для MVP, но имеет риски:

| Угроза | Вероятность | Урон |
|---|---|---|
| РКН блочит IP backend'а в РФ | Средняя | Mini App не открывается, новые юзеры теряются, обновления конфигов рвутся |
| reg.ru получает требование от РКН и сносит наш VPS | Низкая | Полный downtime, теряется БД если без бэкапа |
| Telegram блокируется в РФ | Низкая (но растёт) | Mini App недоступен (требует TG-клиент), но backend работает |
| Аппаратный сбой VPS у reg.ru | Низкая | Downtime пока reg.ru поднимет |

Самодостаточность конфигов в HAPP даёт буфер: уже подключённые юзеры работают через exit-ноды напрямую, backend им не нужен. Но **новые юзеры и обновления конфигов** ломаются.

## 🏗 Архитектура

```
                            sbrf-cdn571.ru (DNS: reg.ru или CF)
                                       │
                  ┌────────────────────┼────────────────────┐
                  │                    │                    │
       api.sbrf-cdn571.ru     api2.sbrf-cdn571.ru   sub.sbrf-cdn571.ru
       (primary, A → RU)      (mirror, A → DE)      (rotates RU → DE)
                  │                    │                    │
                  ▼                    ▼                    │
       ┌───────────────────┐  ┌───────────────────┐         │
       │ 🇷🇺 reg.ru VPS    │  │ 🇩🇪 Hetzner VPS   │         │
       │  (PRIMARY)        │  │  (MIRROR)         │◄────────┘
       │                   │  │                   │
       │  Caddy            │  │  Caddy            │
       │  Gateway          │  │  Gateway          │
       │  Auth/Sub/VPN     │  │  Auth/Sub/VPN     │
       │  Postgres MASTER ─┼──►  Postgres REPLICA │
       │  Redis            │  │  Redis            │
       │                   │  │                   │
       │  bot-webhook ON   │  │  bot-webhook OFF  │
       │  cron ON          │  │  cron OFF         │
       └───────────────────┘  └───────────────────┘
                ▲                       ▲
                └───────────┬───────────┘
                            │
                  ┌─────────────────────┐
                  │ Health-check service│
                  │ (на 3-й машине или  │
                  │  uptimerobot.com)   │
                  └─────────────────────┘
                            │ при падении primary
                            ▼
                  ┌─────────────────────┐
                  │ Switchover script:  │
                  │ 1. Promote replica  │
                  │ 2. Switch DNS A     │
                  │ 3. Re-set TG webhook│
                  │ 4. Включить cron    │
                  └─────────────────────┘
```

## 🧩 Компоненты

### 1. Postgres репликация (master-replica streaming)

**Базовый вариант — встроенная streaming replication:**

Primary:
```ini
# postgresql.conf
wal_level = replica
max_wal_senders = 3
wal_keep_size = 1024
listen_addresses = '*'

# pg_hba.conf
host  replication  replicator  <DE_VPS_IP>/32  scram-sha-256
```

Replica:
```bash
# первичный bootstrap
pg_basebackup -h <RU_IP> -U replicator -D /var/lib/postgresql/16/main -P -R
# создаст standby.signal автоматом
systemctl start postgresql
```

Проверка:
```sql
-- на master:
SELECT * FROM pg_stat_replication;
-- на replica:
SELECT pg_is_in_recovery();  -- t
```

**Продвинутый вариант — Patroni + etcd:**
Если хотим автоматический failover без скриптов руками — ставим [Patroni](https://patroni.readthedocs.io/) (требует etcd-кластер из 3 машин для consensus). Для MVP избыточно, **берём базовый streaming + ручной promote через скрипт**.

### 2. Failover-скрипт (`scripts/failover.sh`)

Запускается на 3-й (управляющей) машине или вручную:

```bash
#!/bin/bash
set -euo pipefail

# 1. Проверить что primary действительно мёртв (не false positive)
if curl -sf https://api.sbrf-cdn571.ru/health --max-time 10; then
    echo "Primary alive, aborting failover"
    exit 1
fi

# 2. Promote replica → master
ssh hetzner "sudo -u postgres pg_ctl promote -D /var/lib/postgresql/16/main"

# 3. Сменить DNS A-запись api.sbrf-cdn571.ru на DE IP
# (через API regru или CF, если NS на CF)
curl -X POST https://api.cloudflare.com/client/v4/zones/$ZONE_ID/dns_records/$RECORD_ID \
  -H "Authorization: Bearer $CF_TOKEN" \
  -d '{"type":"A","name":"api","content":"'$DE_IP'","ttl":60}'

# 4. То же для sub.sbrf-cdn571.ru
# (curl ... аналогично)

# 5. На DE backend: переустановить Telegram bot webhook
ssh hetzner "curl -X POST https://api.telegram.org/bot$TG_TOKEN/setWebhook \
  -d url=https://api.sbrf-cdn571.ru/bot/webhook"

# 6. Включить cron-задачи на DE (cleanup tokens, нотификации)
ssh hetzner "systemctl start vpn-cron.timer"

# 7. Послать алерт в админский чат
curl -X POST https://api.telegram.org/bot$TG_TOKEN/sendMessage \
  -d chat_id=$ADMIN_CHAT -d "text=⚠️ FAILOVER: switched primary to DE"
```

### 3. Health-check (мониторинг primary)

**Бесплатный вариант:** [UptimeRobot](https://uptimerobot.com/) — пинг каждые 5 минут, алерт в Telegram при downtime > 5 минут.

**Свой вариант:** маленький daemon на DE-машине:
```go
// scripts/healthcheck/main.go
for {
    time.Sleep(30 * time.Second)
    if !isAlive("https://api.sbrf-cdn571.ru/health") {
        failureCount++
        if failureCount >= 5 { // 5*30s = 2.5 min downtime
            triggerFailover()
            break
        }
    } else {
        failureCount = 0
    }
}
```

### 4. Failback (возврат на RU после восстановления)

После failover'а: DE стал master, RU мёртв. Когда RU оживает — он отстаёт, его надо восстановить как replica:

```bash
# На RU (после восстановления):
systemctl stop postgresql
rm -rf /var/lib/postgresql/16/main/*
pg_basebackup -h <DE_IP> -U replicator -D /var/lib/postgresql/16/main -P -R
systemctl start postgresql

# Переключить роли обратно (можно когда удобно):
./scripts/switchover.sh ru-primary
```

**Важно**: если переключение произошло, не торопиться возвращаться. Если РКН заблочил RU IP, возвращаться некуда. Сначала разобраться, потом switchover.

### 5. Клиентская логика (Mini App)

Mini App должен сам уметь переключаться между endpoint'ами на случай если DNS пропагация ещё не дошла:

```typescript
// vpn_next/src/lib/api.ts
const ENDPOINTS = [
  'https://api.sbrf-cdn571.ru',
  'https://api2.sbrf-cdn571.ru',
];

async function fetchWithFailover(path: string) {
  for (const base of ENDPOINTS) {
    try {
      const res = await fetch(base + path, { signal: AbortSignal.timeout(5000) });
      if (res.ok) return res;
    } catch (e) {
      continue;
    }
  }
  throw new Error('All endpoints down');
}
```

Кэшировать выбор в `localStorage` чтобы не пробовать мёртвый primary каждый раз.

### 6. Sync статических ресурсов

- **Конфиги Xray (vpn_users)** — в БД, реплицируются автоматически
- **Telegram bot token / JWT_SECRET** — одинаковые на обеих машинах, в `.env`
- **TLS сертификаты Caddy** — каждая машина выпускает свои (LE), не нужно копировать
- **Загруженные файлы (если будут)** — рассмотреть S3-совместимое хранилище (Selectel S3, MinIO)

## 🧩 Этапы

### Этап 1. Подготовка кода под мульти-instance (4-5ч)

- [ ] Сделать Gateway stateless (никаких локальных файлов, всё в БД)
- [ ] Telegram webhook handler — идемпотентный (один update может прийти на оба instance'а во время failover)
- [ ] Cron-задачи (cleanup, notifications) — обернуть в leader election (БД-флаг "я primary")
  ```go
  func (s *Service) acquireLock() bool {
      _, err := s.db.Exec("UPDATE leader SET node_id=$1 WHERE updated_at < NOW() - INTERVAL '60s'", s.nodeID)
      return err == nil
  }
  ```
- [ ] Логи писать с тегом `node_id` (RU/DE)
- [ ] Endpoint `/health` — должен проверять что Postgres достижим, gRPC сервисы живы

### Этап 2. Настройка реплики (3-4ч)

- [ ] Заказать DE VPS (Hetzner CX22, ~€5/мес)
- [ ] A-запись `api2.sbrf-cdn571.ru → <DE_IP>`
- [ ] Поставить Postgres 16, склонировать конфиги с RU
- [ ] Настроить streaming replication (см. секцию Postgres выше)
- [ ] Развернуть всё то же что на primary (docker-compose), но с `IS_REPLICA=true` в env (отключает cron, webhook handler работает но read-only)
- [ ] Проверить: запись на primary видна на replica через 1-2 секунды

### Этап 3. Failover-скрипт (2-3ч)

- [ ] Написать `scripts/failover.sh` (см. выше)
- [ ] Если NS на reg.ru — использовать [REG.API](https://www.reg.ru/reseller/api2doc) для смены A-записей
- [ ] Если NS перевели на Cloudflare — [CF API](https://developers.cloudflare.com/api/) (проще)
- [ ] Скрипт для switchover (плановое переключение, без аварии)
- [ ] Скрипт для failback (вернуть RU как primary)

### Этап 4. Health-check и автоматизация (2-3ч)

- [ ] Поднять health-check daemon на DE машине (или UptimeRobot)
- [ ] При фейле > N минут — автозапуск `failover.sh`
- [ ] Ручной "kill switch" в админ-боте: `/failover` — чтобы можно было переключить вручную если что-то пошло не так

### Этап 5. Клиентская логика failover (2-3ч)

- [ ] Mini App: `fetchWithFailover` (см. выше)
- [ ] Индикатор в UI: "работаем через резервный сервер" (ненавязчивая иконка)
- [ ] HAPP-конфиги тоже должны указывать оба endpoint'а для подписки:
  ```json
  "subscription_url": "https://sub.sbrf-cdn571.ru/<token>"
  ```
  (но тут DNS-failover решает, клиент не знает о mirror)

### Этап 6. Тестирование (3-4ч)

- [ ] Хаос-тест: вырубить primary VPS — замерить RTO
- [ ] Проверить что юзеры с активными VPN-конфигами не замечают разницы
- [ ] Проверить что новые подписки работают на mirror
- [ ] Telegram bot отвечает (webhook переключён)
- [ ] Failback: вернуть primary, убедиться что данные не потеряны
- [ ] Документировать runbook: что делать если...

## ❓ Открытые вопросы

1. **Patroni vs ручной failover** — Patroni добавляет ещё одну зависимость (etcd, 3 машины). Для нашего масштаба — overkill. Берём ручной + автотриггер по health-check.
2. **Где хостить health-check?** UptimeRobot бесплатно на 50 мониторов, удобно. Но привязка к стороннему сервису. Альтернатива — поднять на 3-й маленькой машине (например, в Турции для географической независимости).
3. **DNS provider для быстрого failover** — reg.ru API работает, но TTL надо ставить 60-120сек заранее. Cloudflare API быстрее, переключение мгновенное. Решение: переехать DNS на CF (без proxy) — это упрощает failover и даёт метрики бесплатно.
4. **S3 для загруженных файлов** — пока не нужно (нет загрузок), но если появится аватарки/чеки — подумать о Selectel S3 / MinIO.
5. **Бэкапы Postgres** — даже с replica нужен point-in-time бэкап (на случай "юзер случайно удалил всё"). [pgBackRest](https://pgbackrest.org/) или просто `pg_dump` cron на S3.

## 🗓 Оценка

| Этап | Время | Зависимости |
|---|---|---|
| 1. Stateless код | 4-5ч | — |
| 2. Настройка replica | 3-4ч | Hetzner VPS |
| 3. Failover-скрипт | 2-3ч | этап 2, DNS API |
| 4. Health-check | 2-3ч | этап 3 |
| 5. Клиент failover | 2-3ч | — |
| 6. Тесты | 3-4ч | всё |
| **Итого** | **16-22ч** | + €5/мес mirror VPS |

## 💰 Дополнительные расходы

| Ресурс | Цена |
|---|---|
| DE mirror VPS (Hetzner CX22) | €5/мес |
| Cloudflare DNS (без proxy) | $0 |
| UptimeRobot | $0 (free tier) |
| **Итого +** | **~€5/мес ≈ 500₽/мес** |

## 🔗 Ссылки

- Patroni docs: https://patroni.readthedocs.io/
- Postgres streaming replication: https://www.postgresql.org/docs/current/warm-standby.html
- Cloudflare API: https://developers.cloudflare.com/api/
- REG.RU API: https://www.reg.ru/reseller/api2doc
- UptimeRobot: https://uptimerobot.com/

## 📝 Roadmap

Этот таск **НЕ для MVP**. Очерёдность:

1. **Сейчас**: запускаем по плану [07-fake-cdn-domain.md](./07-fake-cdn-domain.md) — single backend
2. **+ ASAP после запуска**: настроить **холодный бэкап** (`pg_dump` cron на S3 / отдельный сервер). Это 2-3 часа работы — должно быть сделано перед первым реальным юзером.
3. **При росте до >100 платящих юзеров** или **при первом инциденте downtime** — реализуем этот таск (HA с активным mirror).
4. **При тысячах юзеров** — пересматриваем на полноценный k8s/Patroni/multi-region.
