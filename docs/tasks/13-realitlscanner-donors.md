# 13. Donor SNI через RealiTLScanner — заменить `apple.com`

**Дата:** 2026-04-29
**Статус:** 🟡 Обсуждение
**Автор:** Devin + aziz
**Источник:** [habr.com/ru/articles/1021160](https://habr.com/ru/articles/1021160/)

---

## 🎯 Цель

Заменить `apple.com` на 4 «правильных» donor-домена per VPS, найденных
утилитой [RealiTLScanner](https://github.com/XTLS/RealiTLScanner) на
самом сервере.

---

## 📚 Контекст

Сейчас у нас на всех трёх серверах `serverNames: ["apple.com"]`. Xray
сам кидает warning при старте:

```
[Warning] infra/conf: REALITY: Choosing apple, icloud, etc. as the target
may get your IP blocked by the GFW
```

Apple-домены — известный плохой выбор:
- Известны DPI/GFW как «typical bad Reality donor» — есть эвристики которые ловят паттерн «трафик с RU клиента → apple.com через VPS в Нидерландах» (это не Apple CDN маршрут).
- Apple имеет CDN-узлы в РФ (Akamai/Edge) → нормальный пользователь ходит на **локальный IP**, а наш VPS идёт за границу — **аномалия маршрута**.
- В подсети нашего VPS-провайдера (vdsina, Aeza, Hetzner) нет реальных Apple-узлов — DPI понимает что это маскировка.

### Правильный подход

Для каждого VPS отдельно подобрать 4 SNI которые:
1. Лежат в **той же подсети** (или в подсетях того же AS) что и наш
   VPS — `RealiTLScanner` это и ищет.
2. Поддерживают **TLS 1.3 + HTTP/2**.
3. Отдают **реальный контент** на главной странице без редиректов.
4. Находятся **вне РФ** (нет Anycast/Edge в России).

Тогда из России DPI видит «клиент идёт через ТСПУ к
`some-amsterdam-tld.com` (IP `146.103.112.91`)» — это **естественная
маршрутизация** для голландского сайта без РФ-presence. Никакой
аномалии.

---

## 🏗 Решение

### Шаг 1. Установить RealiTLScanner на каждом VPS

```bash
ssh root@<VPS_IP> bash <<'EOF'
cd /tmp
wget -q https://github.com/XTLS/RealiTLScanner/releases/latest/download/RealiTLScanner-linux-64
chmod +x RealiTLScanner-linux-64
./RealiTLScanner-linux-64 -addr <VPS_IP> -port 443 -showFail false -thread 50 \
  | tee /root/sni-scan.log
EOF
```

Сканер проходит подсеть VPS, делает TLS handshake к каждому соседу,
смотрит сертификат + версию TLS + HTTP/2 + редиректы. На выходе —
список «годных» SNI.

### Шаг 2. Выбрать 4 кандидата

Из лога вручную (или скриптом) выбрать 4, которые:
- Имеют валидный сертификат, TLS 1.3, HTTP/2.
- Не ведут на crypto/adult/спам-сайты (риск ban'a).
- Желательно из разных регистраторов / организаций (раскидать риск).
- Не соседи по разным сабдоменам одного домена (один SNI чёрный
  список делает все мёртвыми).

### Шаг 3. Применить

Через скрипт `add_server/rotate-sni.sh` (см. task 12) для каждого
VPS:
1. `UPDATE vpn_servers SET server_names = $1::jsonb WHERE id = $2` —
   массив из 4 SNI.
2. На VPS: переписать `serverNames` в `/opt/xray/config.json`.
3. **Не забыть `dest`!** В Reality `dest` должен быть **одним** из
   `serverNames` (на которой реальный сертификат). По стандарту
   практики — берётся первый из массива:
   ```json
   "dest": "<sni-1>:443",
   "serverNames": ["<sni-1>", "<sni-2>", "<sni-3>", "<sni-4>"]
   ```
4. `docker restart xray` + `ResyncServer`.
5. Проверить логи Xray — warning про apple.com должен исчезнуть.

### Шаг 4. Применить юзерам

Subscription rebuild (юзеры рефрешат через клиент) — придут новые
ссылки с новым SNI. Старые VLESS ссылки тоже работают если SNI в
массиве остался — поэтому **не надо** убирать старый `apple.com`
сразу, можно мигрировать в 2 шага:

1. **Шаг A:** добавить 4 новых SNI В ДОПОЛНЕНИЕ к `apple.com` →
   `serverNames: ["apple.com", "<new-1>", ..., "<new-4>"]`. Старые
   клиенты продолжают работать (apple.com на месте).
2. **Шаг B (через 1-2 недели):** убрать `apple.com`. Те клиенты,
   которые ещё не рефрешили — автоматически перерезолвят ссылку при
   следующем запросе подписки.

---

## ⚠️ Риски

1. **Reality `dest` mismatch с `serverNames`.** Если `dest` ведёт на
   домен, чьего сертификата НЕТ в `serverNames` — handshake упадёт.
   Чёткое правило: `dest = serverNames[0]:443`.
2. **Donor выходит из строя.** Если выбранный SNI закроется или
   сменит TLS-провайдера — этот SNI в массиве станет «мёртвым».
   Поэтому 4 штуки, не одна.
3. **Изменение SNI требует рестарт Xray** → все клиенты потеряют
   текущие коннекты. Делать в окно low-traffic.

---

## 📦 Объём работ

| Шаг | Время |
|---|---|
| Скрипт `add_server/scan-sni.sh` (запуск RealiTLScanner) | 1 час |
| Скрипт `add_server/rotate-sni.sh` (применение) | 1 час |
| Сканирование на 3 VPS | 30 мин (реалтайм) |
| Подбор 4 SNI per VPS вручную | 30 мин |
| Применение + проверка | 30 мин |

Итого: **0.5 дня**.

---

## ⛓ Зависимости

- **Блокирует task 12** — без реальных SNI ротейтить не из чего (нужен
  массив > 1 элемента, иначе random pick вырождается).
- **После task 12** в БД и коде уже массив — task 13 просто наполняет
  его реальными значениями.

Идеальная последовательность:
**12 (struct/migration ready) → 13 (data populated) → live rollout.**

---

## 🗺 Связанные

- Task 12: SNI rotation infra
- [RealiTLScanner](https://github.com/XTLS/RealiTLScanner) — XTLS official tool
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/deploy/scripts/deploy-xray-new.sh" />
