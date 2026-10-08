<p align="center"><img src="docs/assets/selfmail-hero.png" alt="selfmail — собственный сервис транзакционных писем" width="100%"></p>
<p align="center">
  <a href="https://github.com/Elmar006/selfmail/actions/workflows/ci.yaml"><img src="https://github.com/Elmar006/selfmail/actions/workflows/ci.yaml/badge.svg" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-16b8a6" alt="MIT"></a>
  <a href="https://github.com/Elmar006/selfmail/releases/tag/v0.0.1"><img src="https://img.shields.io/badge/version-0.0.1-16b8a6" alt="Версия 0.0.1"></a>
  <img src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white" alt="Go">
  <img src="https://img.shields.io/badge/PostgreSQL-18-4169E1?logo=postgresql&logoColor=white" alt="PostgreSQL">
  <img src="https://img.shields.io/badge/RabbitMQ-4.3-FF6600?logo=rabbitmq&logoColor=white" alt="RabbitMQ">
  <img src="https://img.shields.io/badge/Redis-8-DC382D?logo=redis&logoColor=white" alt="Redis">
</p>
<p align="center"><strong>Ваши приложения. Ваша очередь писем. Ваша инфраструктура.</strong><br><a href="README.md">English</a> · <a href="docs/architecture.md">Архитектура</a> · <a href="docs/integration.md">Интеграция</a> · <a href="docs/operations.md">Развёртывание</a></p>

# selfmail

**selfmail — самостоятельный сервис технических писем для переиспользования в разных приложениях и системах.** Подключите бэкенд через REST, SMTP или Go SDK. Одна установка обслуживает несколько приложений: у каждого свои ключи, домены, лимиты, шаблоны и события доставки.

Автор — **[Эльмар](https://github.com/Elmar006)**. Код открыт под **[MIT](LICENSE)**: разрешены использование, изменение и интеграция в личные и коммерческие проекты. Платный почтовый провайдер не требуется. Сервер, домены и эксплуатацию обеспечивает владелец установки; лицензии зависимостей действуют отдельно.

## Для кого и каких задач

selfmail предназначен для разработчиков и команд, которым нужен общий модуль доставки писем для приложений, API, фоновых задач или внутренних систем. Протокол подключения не зависит от отрасли проекта и языка его бэкенда.

| Задача | Примеры писем |
|---|---|
| Учётные записи | Подтверждение адреса, сброс пароля, изменение профиля |
| Безопасность | Уведомления о входе, изменении доступа, предупреждения |
| События приложения | Изменения статуса, завершение задач, служебные уведомления |
| Документы и отчёты | Сформированные файлы, выгрузки, регулярные отчёты |
| Несколько приложений | Общая установка с отдельным tenant и ключами для каждого приложения |

Разверните selfmail рядом с бэкендом на одном Linux-сервере или на отдельном почтовом сервере. Подключайте приложения к общей установке либо разворачивайте независимые экземпляры. Бизнес-правила, проверку токенов, создание документов и момент отправки определяет приложение; selfmail отвечает за приём задания, очередь, доставку и обратную связь.

## Возможности

- Postfix отправляет письма напрямую на MX серверы получателей в production.
- PostgreSQL хранит задания, квоты, события и transactional outbox; RabbitMQ доставляет задания workers.
- RLS разделяет проекты; ключи имеют scopes и поддерживают отзыв, в том числе в открытой SMTP-сессии.
- Redis ограничивает поток. Есть приоритеты `critical`, `normal`, `bulk`, дневные квоты, отложенная отправка и TTL.
- Поддерживаются UTF-8, HTML, вложения, ограниченные шаблоны и DKIM.
- Статусы сверяются с журналом Postfix и DSN; адреса подавляются; события приходят в подписанных webhooks.
- Независимый зашифрованный журнал и recovery hold защищают от слепой повторной отправки после восстановления БД.
- Есть retention, WAL/PITR через pgBackRest, резервирование доказательств через Restic, Prometheus и Alertmanager.
- Контейнеры, запросы, MIME, очередь, журнал и шаблоны имеют ограничения ресурсов.

**Гарантии:** `202` подтверждает сохранение задания; `submitted` — принятие Postfix; `delivered` — успешный SMTP-ответ сервера назначения. SMTP не гарантирует exactly-once, попадание во входящие или прочтение. Неоднозначная передача получает `submission_unknown` и требует сверки. Подробнее — [архитектура](docs/architecture.md#delivery-guarantees).

## Стек

| Технология | Назначение |
|---|---|
| Go 1.26 | API, SMTP submission, workers, CLI, SDK |
| PostgreSQL 18 / pgx | Состояние, outbox, RLS, квоты, события |
| RabbitMQ 4.3 | Quorum queues, подтверждения публикации, manual ACK, dead letters |
| Redis 8 | Атомарные ограничения скорости |
| Postfix 3.10 | MX-доставка, очередь, повторы, ограничения доменов, DSN |
| pgBackRest / Restic | Зашифрованные копии БД/WAL и независимого журнала |
| Prometheus / Alertmanager | Метрики и уведомления о сбоях |
| Docker Compose / Caddy | Размещение на одном сервере и HTTPS |

Образы закреплены digest, CI actions — commit SHA. [Политика безопасности](docs/security.md) описывает оставшиеся vendor advisories и обновления.

## Локальный запуск

Нужны Git, Docker и Compose **2.24.4+**. Для Linux/WSL initializer нужен OpenSSL. На Windows подходит Docker Desktop.

```sh
git clone https://github.com/Elmar006/selfmail.git
cd selfmail
sh scripts/init-local.sh
docker compose up -d --build --wait
docker compose exec worker selfmail tenant create --name app --domain app.example.test
```

В PowerShell используйте `pwsh -File scripts/init-local.ps1`, если выполнение скриптов разрешено, либо initializer из WSL. Существующая `.env` сохраняется. Сохраните API-ключ: он показывается один раз. Для запроса ниже задайте его в переменной оболочки `SELFMAIL_API_KEY`.

```sh
curl http://localhost:18080/v1/messages \
  -H "Authorization: Bearer $SELFMAIL_API_KEY" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: app:notification:event-42:v1" \
  -d '{"from":"notifications@app.example.test","to":["recipient@example.net"],"subject":"Задача завершена","text":"Ваша задача успешно завершена.","priority":"normal"}'
```

Откройте [локальный ящик](http://localhost:18025). В development все письма направляются в sink и не выходят в Интернет.

| Сервис | Адрес |
|---|---|
| REST API | `http://localhost:18080` |
| SMTP submission | `localhost:1587`; логин — tenant ID, пароль — API-ключ |
| Локальные письма | `http://localhost:18025` |
| RabbitMQ UI | `http://localhost:15673`; credentials из приватной `.env` |
| Prometheus, опционально | `http://localhost:19090` |

## Подключение к своему приложению

Создайте tenant для каждого приложения или выбранной границы изоляции. Выдайте бэкенду ключ с `messages:write,messages:read` и отправляйте задания асинхронно. Idempotency key отражает операцию: `app:notification:EVENT_UUID:v1`, `document-ready:DOCUMENT_UUID:v1`, `password-reset:REQUEST_UUID`. После таймаута повторяйте исходный запрос с тем же ключом.

```go
// import "github.com/Elmar006/selfmail/pkg/client"
mailer, err := client.New("http://selfmail-api:8080", apiKey)
if err != nil { return err }
result, err := mailer.Send(ctx, "document-ready:"+documentID+":v1", client.SendRequest{
    From: "notifications@app.example.test", To: []string{recipientEmail},
    Subject: "Документ готов", Text: "Сформированный документ приложен к письму.",
    Attachments: []client.Attachment{{Filename: "document.pdf", ContentType: "application/pdf", Data: pdfBytes}},
})
```

`selfmail-api` — сетевое имя, назначенное в общей Docker network. Без общей сети используйте опубликованный API/HTTPS. Вместе с изменением состояния приложения сохраняйте email-задачу в его transactional outbox: это связывает фиксацию операции с последующей отправкой письма. [Integration](docs/integration.md) содержит порядок подключения, SMTP/Nodemailer, webhooks и [выбор сборки SDK](docs/integration.md#release-and-sdk-identity).

## Ресурсы

Ориентир небольшой отдельной установки — **2 vCPU, 4 ГиБ RAM, 30 ГиБ SSD**. Для резервирования, мониторинга и размещения рядом с бэкендом начните с **4 vCPU, 8 ГиБ RAM, 60 ГиБ SSD**, предусмотрев независимый backup. Это рекомендации; производительность конкретного VPS проверяется нагрузкой.

Сумма memory ceilings основных контейнеров — **3,75 ГиБ**, с мониторингом, backup и Caddy — **4,5 ГиБ**, без ОС и сборки/тестов. Лимиты не означают постоянное потребление этой памяти. [Resources](docs/resources.md) содержит замеры и расчёт диска.

**Локальные замеры производительности:** два прогона оптимизированной реализации доставили **1200 из 1200 писем за 78,7 и 121,5 секунды**, без ошибок API и лишних копий. Перепроверки исходной реализации заняли **134,0 и 189,0 секунды**. Везде использовались тела 1 КиБ, три проекта, два параллельных клиента, прежние лимиты CPU/RAM и штатный доменный лимит Redis; задержка Postfix перед локальным приёмником временно составляла `0s`. На общем стенде заметен разброс по условиям хранения и нагрузке: это отдельные наблюдения, а не обещание постоянной скорости. Прогон с вложением 2 МиБ случайных данных в каждом письме завершил 96 из 96 за 31,0 секунды.

**Обновлённый релиз 0.0.1** включает пакетные outbox/подтверждения брокера, общие барьеры долговечности для партий записей логов, пакетные SQL-транзакции и ожидание свободного слота воркером. Прежний результат 1200 писем за 114 секунд относится к первоначальному снимку исходного кода. [Отчёт проверки](docs/verification.md) указывает коммиты, на которых получены замеры.

Со штатной задержкой Postfix `1s` на этот единственный приёмник 120 писем доставлены за 121 секунду, приём через API — 23,3/с. Прежние **900 писем за полчаса** — проверка стабильности с намеренно заданной нагрузкой **0,5/с**, **а не предел пропускной способности**. Это результаты конкретных локальных профилей; максимальная скорость и доставка через Интернет ими не установлены. [Методика](docs/benchmarking.md), [результаты и область проверки](docs/verification.md).

Для настоящей отправки нужны статический публичный IP, открытый исходящий порт 25, PTR/A, DNS доменов, SPF/DKIM/DMARC, TLS, входящие DSN и репутация IP. Почтовый сервис может использовать общий с приложением домен или отдельный поддомен. Доступность приложения сама по себе не обеспечивает эти условия доставки.

## Документация

| Документ | Что внутри |
|---|---|
| [Architecture](docs/architecture.md) | Компоненты, состояние, fencing, очереди, сценарии отказа |
| [Integration](docs/integration.md) | REST/SMTP/SDK, ключи, домены, webhooks, outbox приложения |
| [Operations](docs/operations.md) | Развёртывание, DNS, TLS, обновления, мониторинг, retention |
| [Recovery](docs/recovery.md) | Журнал, hold, PITR, encrypted backups, сверка и release |
| [Resources](docs/resources.md) | Лимиты, замеры, память, CPU, рост диска |
| [Benchmarking](docs/benchmarking.md) | Методика замеров, приём и доставка, профили и перегрузка |
| [Security](docs/security.md) | Secrets, границы доверия, зависимости и инфраструктура |
| [Verification](docs/verification.md) | Проверки, исправления аудита и границы готовности |
| [OpenAPI](api/openapi.yaml) | Контракт API |

Технические руководства написаны на английском для разработчиков, переиспользующих проект. Участие — [CONTRIBUTING.md](CONTRIBUTING.md); сообщения об уязвимостях — [SECURITY.md](SECURITY.md).

© 2026 **Эльмар**. Код, документация и оригинальная обложка — [MIT](LICENSE). Лицензии зависимостей — [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
