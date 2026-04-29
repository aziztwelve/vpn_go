# 11. Server-side блок «узнай-свой-IP» эндпоинтов

**Дата:** 2026-04-29
**Статус:** 🟢 Утверждено — готово к имплементации
**Автор:** Devin + aziz
**Источник:** [habr.com/ru/articles/1021160](https://habr.com/ru/articles/1021160/) — UPD2 от Sergei-thinker

---

## 🎯 Цель

Заблокировать на стороне Xray (на каждом активном сервере) обращения к
известным «узнай свой IP» сервисам, чтобы клиентский браузер / десктоп
не мог через `fetch()` увидеть и слить IP нашего VPS в РКН.

---

## 📚 Контекст

Любой сайт или десктоп-приложение, открытое поверх включённого VPN, может
запросить `https://api.ipify.org` (или 16 других подобных эндпоинтов) — и
получить **public IP нашего VPS**. Дальше:

1. Юзер копирует этот IP в любую жалобу / открытую базу.
2. Сторонние боты сканируют такие базы, отдают РКН/ТСПУ, IP идёт в
   блок-листы.
3. Один скомпрометированный VPS = блокировка тысяч живых юзеров.

Особо опасно потому что **юзер не знает что это происходит** — современные
SPA постоянно тянут с десятка domain'ов и `ipify` могут запросить
«мимоходом» (например, аналитика).

В прод-логах Xray мы регулярно видим:

```
REALITY: processed invalid connection from 104.23.243.244:12688: server name mismatch: squegich.info
```

— это **сканеры** уже нашли наш IP и пробуют разные SNI. Закрытие IP-leak
снижает поток таких сканеров.

---

## 🏗 Решение

В `config.json` каждого Xray-инстанса добавить routing rule, отправляющую
запросы к перечисленным доменам в `outboundTag: "blocked"` (blackhole).

### Список доменов (15 штук, можно расширить)

| Категория | Домены |
|---|---|
| Стандартные «what is my IP» | `ipify.org`, `api.ipify.org`, `ifconfig.me`, `icanhazip.com`, `ident.me`, `myexternalip.com`, `wtfismyip.com` |
| Geo-API | `ipinfo.io`, `ipapi.co`, `ipwhois.app`, `ip-api.com`, `api.myip.com` |
| RU-пользовательские | `2ip.ru`, `2ip.io` |
| Прочее | `ip.sb`, `checkip.amazonaws.com`, `redirector.googlevideo.com` (геолокация YouTube) |

### Изменение в `routing.rules`

Сейчас:
```json
"routing": {
  "rules": [
    {"type":"field","inboundTag":["api"],"outboundTag":"api"}
  ]
}
```

Стало:
```json
"routing": {
  "rules": [
    {"type":"field","inboundTag":["api"],"outboundTag":"api"},
    {
      "type":"field",
      "domain":[
        "ipify.org","api.ipify.org","ifconfig.me","icanhazip.com",
        "ident.me","myexternalip.com","wtfismyip.com",
        "ipinfo.io","ipapi.co","ipwhois.app","ip-api.com","api.myip.com",
        "2ip.ru","2ip.io","ip.sb","checkip.amazonaws.com",
        "redirector.googlevideo.com"
      ],
      "outboundTag":"blocked"
    }
  ]
}
```

`blocked` outbound уже есть в нашем шаблоне (`{"protocol":"blackhole","tag":"blocked"}`).

---

## 🛠 Имплементация

### Шаг 1. Шаблон в скрипте
- В <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/deploy/scripts/deploy-xray-new.sh" /> добавить routing rule с блокировкой эндпоинтов в генерируемый `config.json`. Все будущие серверы будут уже с фильтром.

### Шаг 2. Применить на live-серверах
Скрипт `add_server/apply-ip-leak-block.sh` (новый):
1. Для каждого VPS в `vpn_servers WHERE is_active=true`:
   - Скачать текущий `/opt/xray/config.json` (либо через `vpn-xray` для Germany).
   - Вставить новое правило в `routing.rules`.
   - Залить обратно.
   - `docker restart xray`.
   - Дёрнуть `vpn.v1.VPNService/ResyncServer {server_id}` (после рестарта `clients[]` обнуляется).

### Шаг 3. Локальный Xray (Germany)
Для контейнера `vpn-xray` обновить файл-источник
<ref_file file="/root/.openclaw/workspace/vpn/vpn_go/deploy/compose/xray/config.json" />
и перезапустить контейнер через `docker compose restart vpn-xray`.

---

## ✅ Проверка

С клиента под VPN:
```bash
curl -m 5 https://api.ipify.org/  # должно вернуть таймаут / connection reset
curl -m 5 https://ifconfig.me/    # то же
curl -m 5 https://google.com/     # должно работать (не в блок-листе)
```

Для Yandex / VK / банков — тоже должны работать (split routing на клиенте,
он у нас уже настроен через subscription modes).

В логах Xray должно появиться:
```
[Info] app/proxyman/outbound: ... taking detour [blocked]
```

---

## ⚠️ Риски и ограничения

1. **Список не покрывает всех.** Если кто-то запустит self-hosted
   ip-чекер на свежем домене — мы его не блокируем. Защита **снижает**
   поверхность атаки, не закрывает на 100%.
2. **Yandex может ходить через `ipapi.co`/`ipinfo.io`?** Нет — Yandex
   определяет геолокацию через **свои** API (`yandex.ru/internet`,
   например), которых нет в списке. Если что — добавим в whitelist.
3. **`redirector.googlevideo.com`** — YouTube использует его для CDN
   geo-разруливания. Блокировка может сломать видео-плеер. **Проверить
   на проде** перед раскаткой и при необходимости убрать только этот.

---

## 📦 Объём работ

- Скрипт `apply-ip-leak-block.sh`: ~50 строк bash.
- Правка `deploy-xray-new.sh`: 1 правило в JSON-блоке.
- Правка `deploy/compose/xray/config.json`: то же.
- Применение на 3 live VPS + рестарт + Resync: ~10 минут.

Итого: **1–2 часа работы**.

---

## 🗺 Связанные файлы

- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/deploy/scripts/deploy-xray-new.sh" />
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/deploy/compose/xray/config.json" />
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/add_server/README.md" />
