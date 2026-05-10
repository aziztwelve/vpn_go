# Tasks

Рабочие задачи и планы реализации. В отличие от `specs/` (описание "как должно быть"), тут хранится **что делаем сейчас** и **почему**.

## Формат

Каждая задача — отдельный markdown файл с префиксом-номером:

```
01-mvp-plan.md
02-<название следующей задачи>.md
...
```

В начале файла:
- **Дата** создания
- **Статус:** 🟡 Обсуждение / 🟢 Утверждено / 🔵 В работе / ✅ Готово / ❌ Отменено
- **Автор**

## Текущие задачи

- [01-mvp-plan.md](./01-mvp-plan.md) — 🟢 MVP план: Утверждён Вариант C
- [02-mvp-c-implementation.md](./02-mvp-c-implementation.md) — 🔵 Реализация Варианта C (в работе)
- [03-client-config-smart-routing.md](./03-client-config-smart-routing.md) — 🟡 Умный клиентский конфиг (split-tunnel RU → direct)
- [04-caddy-auto-tls.md](./04-caddy-auto-tls.md) — 🟢 Публичный домен + авто-TLS через Caddy (заменяет 9.2/9.7)
- [05-trial-period.md](./05-trial-period.md) — 🟢 Автоматический пробный период 3 дня новым юзерам
- [06-traffic-caps.md](./06-traffic-caps.md) — 🟡 Лимиты трафика на тариф (против перепродажи без per-device UUID)
- [07-fake-cdn-domain.md](./07-fake-cdn-domain.md) — ⏸ Отложено: фейк-CDN домен sbrf-cdn571.ru (Reality+sni=github.com уже решает задачу)
- [08-ha-backend-mirror.md](./08-ha-backend-mirror.md) — ⏸ Отложено: HA backend с failover (зависит от 07)
- [09-plans-v2.md](./09-plans-v2.md) — 🔵 `/plans.v2`: редизайн витрины тарифов + бизнес-фичи (промокоды, автопродление, trial-бэйдж, pending-flow)
- [10-yoomoney-quickpay.md](./10-yoomoney-quickpay.md) — 🟢 ЮMoney Quickpay: приём картой ₽ через форму + HTTP-уведомления (без OAuth, fix существующих багов) — в работе
- [11-ip-leak-block.md](./11-ip-leak-block.md) — 🟢 Server-side блок 17 «узнай-свой-IP» эндпоинтов (Habr UPD2, защита IP VPS от утечки)
- [12-sni-rotation.md](./12-sni-rotation.md) — 🟡 SNI rotation: 4 `serverNames` per inbound + миграция `server_names` в JSONB-array
- [13-realitlscanner-donors.md](./13-realitlscanner-donors.md) — 🟡 Donor SNI через RealiTLScanner — заменить `apple.com` на 4 локальных донора per VPS (зависит от 12)
- [14-retire-germany-prod.md](./14-retire-germany-prod.md) — 🟡 Снять Germany (Hetzner) с prod-роутинга, оставить как dev/code-сервер (бэкенд + локальный Xray для тестов)
- [15-retention-campaigns.md](./15-retention-campaigns.md) — 🟡 Retention-кампании (trial-ending, onboarding, churn) — segmentation engine + draft-approve flow
- [16-rkn-resilience.md](./16-rkn-resilience.md) — 🟡 RKN-устойчивость control-plane: RU-mirror подписки, multi-domain, anonymous registrar, origin IP whitelist
- [17-ru-vps-lte.md](./17-ru-vps-lte.md) — 🟡 RU-VPS для LTE-обхода (Yandex.Cloud) — Hetzner DE не пробивает агрессивный DPI операторов в Хакасии/Сибири, разведка инфры конкурента (geodataload.com)
- [18-bot-reply-keyboard.md](./18-bot-reply-keyboard.md) — 🔵 Reply-keyboard в боте: «🌐 Подключиться» + «🛒 Купить подписку» (заимствование UX EXTRA VPN, см. research/competitor-extravpn.md)
- [end_sni.md](./end_sni.md) — 🟢 **Финальный план SNI/RU-инфра (endgame)** — объединяет 12+13+17 в пошаговый roadmap: домен `qubexlabs.com` под VPN-ноды (✅ Stage 2 готов: домен + CF + лендинг), далее MWS RU-VPS, RealiTLScanner SNI-пулы, per-server keys
