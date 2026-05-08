#!/usr/bin/env bash
# pg-backup.sh — почасовой бэкап Postgres + rsync на удалённый VPS.
#
# Запускается из cron каждый час. Делает pg_dump (plain SQL + gzip -9),
# складывает в $LOCAL_DIR с timestamp'ом, удаляет файлы старше $KEEP_HOURS,
# затем rsync'ит весь $LOCAL_DIR на $REMOTE.
#
# Восстановление:
#   gunzip -c vpn-YYYYMMDD-HHMMSS.sql.gz | docker exec -i vpn-postgres psql -U vpn -d vpn
#
# Если бэкап провалился — exit code != 0, cron пришлёт mail (если настроен)
# или логи в $LOG_FILE.

set -euo pipefail

# ────────────── Configuration ──────────────
LOCAL_DIR="${PG_BACKUP_DIR:-/opt/backups/postgres}"
LOG_FILE="${PG_BACKUP_LOG:-/var/log/pg-backup.log}"
REMOTE="${PG_BACKUP_REMOTE:-nl01:/root/backups/vpn-postgres/}"
CONTAINER="${PG_BACKUP_CONTAINER:-vpn-postgres}"
DB_USER="${PG_BACKUP_USER:-vpn}"
DB_NAME="${PG_BACKUP_DB:-vpn}"
KEEP_HOURS="${PG_BACKUP_KEEP_HOURS:-168}"   # 7 дней почасовых
MIN_BYTES="${PG_BACKUP_MIN_BYTES:-1024}"    # минимум 1 KB — иначе считаем дамп невалидным

# ────────────── Setup ──────────────
mkdir -p "$LOCAL_DIR"
mkdir -p "$(dirname "$LOG_FILE")"

ts() { date '+%Y-%m-%d %H:%M:%S'; }
log() { echo "$(ts) $*" | tee -a "$LOG_FILE"; }

# Lock to prevent concurrent runs
LOCKFILE="/var/run/pg-backup.lock"
exec 9>"$LOCKFILE"
if ! flock -n 9; then
    log "ERROR: another pg-backup is running; abort"
    exit 1
fi

TS=$(date -u +%Y%m%d-%H%M%S)
FILE="vpn-${TS}.sql.gz"
TMP="$LOCAL_DIR/.${FILE}.tmp"
DST="$LOCAL_DIR/$FILE"

# ────────────── Dump ──────────────
log "START $FILE"

# pg_dump из контейнера → gzip на хосте.
# Используем plain SQL (--format=plain по умолчанию) + gzip:
# - читаемо, можно head/grep
# - сжатие ~80–90% для нашей БД (текстовые JSON, индексы)
# - portable: восстанавливается на любой версии Postgres
if ! docker exec "$CONTAINER" pg_dump \
        -U "$DB_USER" -d "$DB_NAME" \
        --no-owner --no-acl --clean --if-exists \
        | gzip -9 > "$TMP"; then
    log "ERROR pg_dump failed"
    rm -f "$TMP"
    exit 2
fi

SIZE=$(stat -c%s "$TMP" 2>/dev/null || echo 0)
if [ "$SIZE" -lt "$MIN_BYTES" ]; then
    log "ERROR dump too small: $SIZE bytes (< $MIN_BYTES)"
    rm -f "$TMP"
    exit 3
fi

mv "$TMP" "$DST"
log "DUMP ok size=${SIZE}B file=$FILE"

# ────────────── Rotate ──────────────
# Удаляем всё кроме последних $KEEP_HOURS файлов
removed=$(cd "$LOCAL_DIR" && ls -1t vpn-*.sql.gz 2>/dev/null | tail -n +$((KEEP_HOURS + 1)) | tee /tmp/.pg-backup-removed | wc -l || echo 0)
if [ "$removed" -gt 0 ]; then
    (cd "$LOCAL_DIR" && xargs -r rm -f < /tmp/.pg-backup-removed)
    log "ROTATE removed=$removed kept=$KEEP_HOURS"
fi
rm -f /tmp/.pg-backup-removed

# ────────────── Sync to remote ──────────────
# rsync с --delete-after — удаляет на remote всё, чего нет локально (после ротации)
# -a архивный режим, -z сжатие в transit (не нужно — gzip уже сжат, но не вредит для метаданных)
# --partial — на случай разрыва, продолжит
if rsync -a --delete-after --partial \
        -e 'ssh -o ConnectTimeout=10 -o BatchMode=yes' \
        "$LOCAL_DIR/" "$REMOTE" >> "$LOG_FILE" 2>&1; then
    log "RSYNC ok → $REMOTE"
else
    rc=$?
    log "WARN rsync failed rc=$rc (local backup is OK)"
    # Не падаем — локальный бэкап есть, remote sync можно повторить позже
fi

log "DONE $FILE"
