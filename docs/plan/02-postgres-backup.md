# 02. Postgres backup — реализация

**Дата:** 2026-05-03
**Статус:** ✅ Внедрено в проде
**Связан с:** [`00-ha-scaling-roadmap.md`](./00-ha-scaling-roadmap.md) → задача **0.1**

---

## 🎯 Что сделано

Hourly `pg_dump` локально на backend-VPS + автоматический `rsync` на удалённый VPS (`nl01`, Нидерланды) для гео-распределения. Бесплатно, без сторонних S3.

---

## 🏗 Архитектура

```
┌──────────────────────────────────────┐
│  vpn-postgres (Docker, на backend)   │
└───────────────┬──────────────────────┘
                │ docker exec pg_dump
                ▼
┌──────────────────────────────────────┐
│  backend VPS (DE Falkenstein)        │
│  /opt/backups/postgres/              │
│  vpn-YYYYMMDD-HHMMSS.sql.gz × 168    │  ← 7 дней почасовых
│  cron: 7 */1 * * *                   │
└───────────────┬──────────────────────┘
                │ rsync (ssh)
                ▼
┌──────────────────────────────────────┐
│  nl01 VPS (NL)                       │
│  /root/backups/vpn-postgres/         │
│  (зеркало, --delete-after)           │
└──────────────────────────────────────┘
```

**Почему такой выбор:**
- **Локально + удалённо:** даже если backend-VPS целиком умрёт (диск, провайдер) — есть копия в другой стране.
- **`nl01` уже есть** в SSH-конфиге, на нём есть rsync, 5.8 GB свободно (полная цепочка 7 дней × почасовых ≈ 10 MB × 168 ≈ 1.7 GB при росте БД до 100 MB).
- **Без S3:** не нужно платить, не нужно заводить аккаунты, не нужно хранить access keys в CI/секретах. Всё на SSH-ключах, которые уже есть.
- **Plain SQL + gzip:** дамп читаемый (`gunzip -c | head`), restore работает на любой версии Postgres ≥ 13.

---

## 📦 Компоненты

### 1. Скрипт
[`vpn_go/deploy/scripts/pg-backup.sh`](../../deploy/scripts/pg-backup.sh) — версионируется в репо.

Установлен в проде как симлинк:
```
/usr/local/bin/pg-backup.sh -> /root/.openclaw/workspace/vpn/vpn_go/deploy/scripts/pg-backup.sh
```

При деплое новой версии скрипта — он подхватится автоматически (cron бьёт по симлинку).

### 2. Cron
```cron
7 * * * * /usr/local/bin/pg-backup.sh >> /var/log/pg-backup.log 2>&1
```

Каждый час на 7-й минуте. 7-я минута выбрана чтобы не совпадать с другими типичными 0/15/30/45.

### 3. Logrotate
`/etc/logrotate.d/pg-backup`:
```
/var/log/pg-backup.log {
    weekly
    rotate 4
    compress
    delaycompress
    missingok
    notifempty
    copytruncate
    su root root
}
```

Лог не будет расти бесконечно (4 недели по weekly).

### 4. Параметры скрипта (override через env)

| Переменная | Default | Что |
|---|---|---|
| `PG_BACKUP_DIR` | `/opt/backups/postgres` | Локальный каталог |
| `PG_BACKUP_LOG` | `/var/log/pg-backup.log` | Лог-файл |
| `PG_BACKUP_REMOTE` | `nl01:/root/backups/vpn-postgres/` | rsync-таргет (формат ssh-конфига) |
| `PG_BACKUP_CONTAINER` | `vpn-postgres` | Имя контейнера Postgres |
| `PG_BACKUP_USER` | `vpn` | Postgres user |
| `PG_BACKUP_DB` | `vpn` | Имя БД |
| `PG_BACKUP_KEEP_HOURS` | `168` | Сколько часовых дампов хранить (7 дней) |
| `PG_BACKUP_MIN_BYTES` | `1024` | Минимальный размер валидного дампа |

---

## 🔬 Восстановление

### Вариант 1 — на той же машине (rollback на час назад)

```bash
# Найти последний валидный бэкап
ls -lt /opt/backups/postgres/

# Восстановить
gunzip -c /opt/backups/postgres/vpn-20260503-010738.sql.gz \
    | docker exec -i vpn-postgres psql -U vpn -d vpn
```

Скрипт делает `--clean --if-exists`, поэтому restore «накатывается поверх» — DROP TABLE IF EXISTS + CREATE.

⚠️ **Перед restore'ом всегда сделать `pg_dump` текущего состояния** на случай если решишь откатиться обратно.

### Вариант 2 — на новой машине (DR)

Если backend-VPS целиком потерян:

```bash
# 1. На любой машине поднять Postgres (можно через docker-compose из репо)
docker compose -f docker-compose.yml up -d postgres

# 2. Скопировать последний бэкап с nl01
scp nl01:/root/backups/vpn-postgres/vpn-20260503-010738.sql.gz .

# 3. Восстановить
gunzip -c vpn-20260503-010738.sql.gz \
    | docker exec -i vpn-postgres psql -U vpn -d vpn

# 4. Проверить целостность
docker exec vpn-postgres psql -U vpn -d vpn -c "
    SELECT 'users', count(*) FROM users
    UNION ALL SELECT 'payments', count(*) FROM payments
    UNION ALL SELECT 'subscriptions', count(*) FROM subscriptions;
"
```

### Вариант 3 — точечно (одна таблица)

```bash
# Достать только COPY-секцию для нужной таблицы
gunzip -c vpn-20260503-010738.sql.gz \
    | awk '/^COPY public.payments/,/^\\\.$/' \
    > payments.sql

# Накатить (предварительно очистив таблицу или в отдельную БД)
```

---

## 🧪 Регулярная проверка (раз в неделю)

Без проверки бэкапы могут оказаться битыми. Добавить в `HEARTBEAT.md` или отдельный cron:

```bash
# Берём свежий, делаем restore в test-БД, проверяем количество строк
LATEST=$(ls -t /opt/backups/postgres/vpn-*.sql.gz | head -1)
docker exec vpn-postgres psql -U vpn -d postgres -c "DROP DATABASE IF EXISTS vpn_test;"
docker exec vpn-postgres psql -U vpn -d postgres -c "CREATE DATABASE vpn_test;"
gunzip -c "$LATEST" | docker exec -i vpn-postgres psql -U vpn -d vpn_test
docker exec vpn-postgres psql -U vpn -d vpn_test -c "SELECT count(*) FROM users;"
docker exec vpn-postgres psql -U vpn -d postgres -c "DROP DATABASE vpn_test;"
```

---

## 📊 Текущие цифры (на момент внедрения)

- **Сжатый размер дампа:** 30 KB (БД 9.4 MB, plain SQL ~250 KB → gzip ~30 KB)
- **Время дампа+gzip+rsync:** < 2 секунды
- **Нагрузка на Postgres:** незаметная (`pg_dump` берёт shared lock, не блокирует записи)
- **Место на 7 дней:** ~5 MB локально + 5 MB на nl01

При росте БД до 100 MB размер дампа будет ~10 MB, на 7 дней почасовых — 1.7 GB. Свободного места хватает с запасом.

---

## 🔍 Мониторинг здоровья бэкапов

Простая проверка «свежести»:
```bash
# Сколько минут назад был последний успешный бэкап?
LATEST=$(ls -t /opt/backups/postgres/vpn-*.sql.gz 2>/dev/null | head -1)
[ -z "$LATEST" ] && echo "NO BACKUPS" && exit 1
AGE=$(( ($(date +%s) - $(stat -c%Y "$LATEST")) / 60 ))
echo "Last backup: $AGE minutes ago"
[ "$AGE" -gt 75 ] && echo "WARN: backup is stale" && exit 1
```

Когда подключим Prometheus — экспортировать metric `pg_backup_last_success_timestamp` и алерт «> 90 минут без бэкапа».

---

## ❓ Что ещё стоит сделать (не сейчас)

- **WAL archiving (`archive_mode = on`)** — для point-in-time recovery с точностью до секунды. Нужно при ценности данных «нельзя терять даже 1 час». Сейчас БД мелкая и потеря 1 часа = 1–10 строк, нецелесообразно.
- **3-я копия в другой стране/у другого провайдера** — например, на `fi02` (Финляндия). Сейчас если сразу DE и NL отвалятся — теряем всё. Маловероятно, но добавить можно одной строкой rsync (`PG_BACKUP_REMOTE` поддерживает только один target — нужно расширить скрипт).
- **Тест восстановления раз в неделю автоматически** (см. секцию выше) — поднимать `vpn_test` БД и проверять `count(*)`. Алерт если строк меньше чем в основной.
- **Шифрование дампов** (`gpg` с ключом, доступным только админу) — если БД содержит чувствительные данные. Сейчас telegram_id + first_name — не super-sensitive, можно отложить.

---

## 🗺 Где это в коде

| Файл | Что |
|---|---|
| [`deploy/scripts/pg-backup.sh`](../../deploy/scripts/pg-backup.sh) | Сам скрипт (версионирован в репо) |
| `/usr/local/bin/pg-backup.sh` | Симлинк на скрипт из репо |
| `crontab -l` (root) | `7 * * * * /usr/local/bin/pg-backup.sh ...` |
| `/etc/logrotate.d/pg-backup` | Ротация логов |
| `/opt/backups/postgres/` | Локальный архив (7 дней) |
| `nl01:/root/backups/vpn-postgres/` | Удалённое зеркало |
| `/var/log/pg-backup.log` | Лог запусков |
