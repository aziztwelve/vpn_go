#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# Генерирует per-service .env файлы из мастера deploy/env/.env и шаблонов
# deploy/env/<service>.env.template.
#
# Usage:
#   SERVICES="auth,sub,vpn,gateway" ENV_SUBST=./bin/envsubst ./deploy/env/generate-env.sh
#
# Или через Taskfile:
#   task env:generate
# -----------------------------------------------------------------------------
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMPLATE_DIR="$SCRIPT_DIR"

# envsubst: либо переданный путь (из Taskfile), либо системный
if [ -z "${ENV_SUBST:-}" ]; then
  if ! command -v envsubst &>/dev/null; then
    echo "❌ envsubst не найден. Установи через 'task env:install-envsubst' или поставь системный пакет gettext." >&2
    exit 1
  fi
  ENV_SUBST=envsubst
fi

MASTER_ENV="$SCRIPT_DIR/.env"
MASTER_TEMPLATE="$SCRIPT_DIR/.env.template"

if [ ! -f "$MASTER_ENV" ]; then
  echo "🔄 Мастер $MASTER_ENV не найден — создаю из .env.template"
  cp "$MASTER_TEMPLATE" "$MASTER_ENV"
else
  # Дописать ключи, которые есть в шаблоне, но отсутствуют в .env.
  # Это безопасный мёрдж — существующие значения не трогаем.
  added=0
  while IFS='=' read -r key _rest; do
    # пропускаем пустые строки и комментарии
    [[ -z "$key" || "$key" == \#* ]] && continue
    if ! grep -qE "^${key}=" "$MASTER_ENV"; then
      line=$(grep -E "^${key}=" "$MASTER_TEMPLATE" | head -n1)
      printf '\n%s\n' "$line" >>"$MASTER_ENV"
      echo "  ➕ добавил в $MASTER_ENV: $key"
      added=$((added + 1))
    fi
  done < <(grep -E '^[A-Z_][A-Z0-9_]*=' "$MASTER_TEMPLATE")
  if [ $added -gt 0 ]; then
    echo "🔄 Мастер .env синхронизирован с шаблоном (добавлено ключей: $added)"
  fi
fi

# Экспортируем все переменные из мастер-.env (для envsubst)
set -a
# shellcheck disable=SC1090
source "$MASTER_ENV"
set +a

SERVICES_CSV="${SERVICES:-auth,sub,vpn,gateway}"
IFS=',' read -ra SERVICES_ARR <<<"$SERVICES_CSV"

echo "🔍 Генерирую .env для: ${SERVICES_ARR[*]}"

success=0
skipped=0
for svc in "${SERVICES_ARR[@]}"; do
  tmpl="$TEMPLATE_DIR/${svc}.env.template"
  out="$TEMPLATE_DIR/${svc}.env"

  if [ ! -f "$tmpl" ]; then
    echo "  ⚠️  $tmpl не найден — пропускаю"
    skipped=$((skipped + 1))
    continue
  fi

  "$ENV_SUBST" <"$tmpl" >"$out"
  echo "  ✅ $out"
  success=$((success + 1))
done

# Дополнительно: генерируем Xray config.json из шаблона
XRAY_TMPL="$SCRIPT_DIR/../compose/xray/config.json.template"
XRAY_OUT="$SCRIPT_DIR/../compose/xray/config.json"
if [ -f "$XRAY_TMPL" ]; then
  "$ENV_SUBST" <"$XRAY_TMPL" >"$XRAY_OUT"
  echo "  ✅ $XRAY_OUT"
  success=$((success + 1))
fi

echo "🎉 Готово: создано $success, пропущено $skipped"
