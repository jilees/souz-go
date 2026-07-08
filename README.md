# souz-go

Go-порт агентного ядра [souz](https://github.com/D00mch/souz) (Kotlin Multiplatform) для встраиваемых устройств — Android-based ARM64 Linux со 256 MB RAM и 30 MB свободного места, куда JVM просто не помещается.

Идея простая: тот же граф-движок агента (classify → инструменты → LLM → tool-loop → summarize), что и в оригинале, но без JVM, без Docker-песочницы, без multi-tenant backend — статический Go-бинарник на ~7 MB, который можно залить на встраиваемое устройство и держать там постоянно через `monit`.

## Статус

Проект — рабочий, но не полная копия Kotlin-оригинала. Реализовано целиком:

- граф-движок агента и полный ход (classify → MCP → skills → enrich → LLM → tool-loop → summarize)
- три LLM-провайдера: Anthropic, любой OpenAI-совместимый (OpenAI/OpenRouter/Qwen/AiTunnel/...), Codex (ChatGPT-подписка через OAuth device-flow)
- локальные инструменты: файлы (сендбокс), веб (fetch + поиск), калькулятор
- MCP-клиент (stdio + HTTP+SSE)
- полный пайплайн скиллов: парсинг бандлов, structural/static/LLM-валидация с кэшированием вердиктов, LLM-селектор, выполнение через `RunSkillCommand`
- Telegram-канал (long-polling)
- HTTP API (`/v1/**`), совместимый по форме с KMP-клиентом, но не 1:1 — см. ниже
- атомарное JSON-хранилище сессий, YAML-конфиг

Сознательно **не реализовано** (см. `docs/plan.md`):

- **Вендорский голосовой канал** (`pkg/channels/sberboom/`) и **Mattermost-канал** (`pkg/channels/mattermost/`) — заглушки, `Start()`/`Send()` ничего не делают, в `cmd/souz-agent/wiring.go` даже не подключены. Голосового канала к OS-мосту устройства пока нет — агент доступен только по HTTP API и через Telegram.
- `/v1/me/settings`, `/v1/me/provider-keys`, `/v1/onboarding/**` и прочие multi-tenant-роуты — однопользовательский embedded-агент настраивается через `config.yaml`, а не через HTTP.
- GigaChat, voice API, PostgreSQL, Docker-песочница, desktop-инструменты (браузер, календарь, почта, буфер обмена и т.п.) — исключены изначально, не входят в объём порта.

## Архитектура

```
classify → inject MCP tools → inject skills → enrich context → LLM call → tool loop → summarize
```

`pkg/graph/` — framework-free типизированный граф-раннер, ничего не знает про агентов/LLM. `AgentContext` неизменяем: каждый узел графа возвращает новую копию (value semantics, без разделяемого состояния). `pkg/bus/` соединяет каналы с исполнителем агента через буферизованные Go-каналы.

```
cmd/souz-agent/     ← точка входа: DI-сборка, graceful shutdown
pkg/
  bus/              ← MessageBus: буферизованный chan-роутинг (inbound/outbound)
  channels/
    channel.go      ← интерфейс Channel + BaseChannel (allow-list)
    telegram/       ← long-polling бот (реализован)
    sberboom/       ← WebSocket-клиент к OS-мосту устройства (заглушка)
    mattermost/     ← WebSocket real-time (заглушка)
  graph/            ← типизированный граф-раннер общего назначения
  agent/
    context.go      ← AgentContext (неизменяемый снапшот состояния хода)
    executor.go     ← один ход агента: вход → выход
    nodes/          ← узлы графа: classify, enrich, llm, toolloop, summarize, mcp, skills
  providers/
    types.go        ← интерфейс LLMProvider + DTO ChatRequest/Response/Message
    anthropic/      ← Anthropic Messages API (стриминг)
    openai_compat/  ← OpenAI-совместимый API (OpenAI, Qwen, AiTunnel, OpenRouter, ...)
    codex/          ← ChatGPT-подписка через OAuth device-flow (Responses API)
  tools/
    tool.go         ← интерфейс Tool
    files/          ← чтение/запись/поиск файлов, сендбокс
    web/             ← HTTP-fetch + поиск
    math/            ← калькулятор
    skills/          ← RunSkillCommand
  mcp/              ← MCP-клиент: stdio + HTTP+SSE транспорты
  skills/
    bundle/          ← парсер SkillBundle, SKILL.md YAML frontmatter
    validation/       ← structural → static → LLM валидация
    selection/         ← LLM-селектор релевантных скиллов
    registry/           ← файловый реестр скиллов
  storage/          ← атомарное JSON-хранилище сессий
  config/           ← загрузчик YAML-конфига, SecureString
  http/             ← /v1/** HTTP API + SSE event sink
```

## Быстрый старт

```bash
# Запуск локально
go run ./cmd/souz-agent

# Обычная сборка
go build ./cmd/souz-agent

# Кросс-компиляция под целевое устройство (ARM64 Linux, статический бинарник)
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o souz-agent-arm64 ./cmd/souz-agent

# Тесты
go test ./...

# Линт
golangci-lint run
```

Кросс-компилированный бинарник — статический ELF ARM64, **~7 MB** (цель из CLAUDE.md: ≤20 MB, ≤12 MB с UPX).

## Конфигурация

`config.yaml` (по умолчанию `~/.local/state/souz-go/config.yaml`, переопределяется флагом `-config`):

```yaml
dataDir: /path/to/state

provider: openai_compat   # anthropic | openai_compat | codex
model: gpt-5              # или любая модель провайдера
temperature: 0.7
contextSize: 16000
maxTokens: 4096

openaiCompat:
  apiKey: sk-...
  baseURL: https://api.openai.com/v1   # можно указать OpenRouter/Qwen/AiTunnel и т.п.

tools:
  webEnabled: true
  filesRoot: ""            # пусто — files-тул не регистрируется

http:
  enabled: true
  addr: ":8080"

skills:
  enabled: true

telegram:
  enabled: false
  token: ""
  allowFrom: []             # пусто = бот отвечает всем; для личного использования указать свой user id
```

Провайдер `codex` не требует ключа в конфиге — авторизация через OAuth device-flow:

```bash
souz-agent -codex-login
```

Токен пишется отдельно (`{dataDir}/codex-token.json`), не в `config.yaml`.

## HTTP API

`/v1/**` — сокращённый, но по форме совместимый с KMP-клиентом набор: camelCase-поля, error envelope `{"error":{"code","message"}}`, тот же словарь SSE-событий (`message.delta`, `execution.started/finished/failed/cancelled`, `tool.call.*`). Полной 1:1 копии `BackendHttpRoutes.kt` нет — см. "Статус" выше.

| Метод | Путь | Назначение |
|---|---|---|
| GET | `/health` | health-check |
| GET | `/v1/chats` | список чатов |
| POST | `/v1/chats` | создать чат |
| GET | `/v1/chats/{chatId}/messages` | история сообщений |
| POST | `/v1/chats/{chatId}/messages` | отправить сообщение (`{"content":"..."}`) |
| GET | `/v1/chats/{chatId}/events` | SSE-поток событий выполнения |
| POST | `/v1/chats/{chatId}/cancel-active` | отменить текущее выполнение |

Однопользовательский режим: `userId = "default"` везде, auth-заголовки принимаются, но игнорируются — аутентификации нет, наружу порт пробрасывать не стоит.

## Инструменты

| Инструмент | Назначение |
|---|---|
| `read_file`, `list_files`, `write_file`, `search_files` | файловый доступ, сендбокс по `tools.filesRoot`, защита от `..`/symlink-escape |
| `web_fetch`, `web_search` | HTTP fetch (HTML→текст) и поиск (DuckDuckGo HTML, без API-ключа) |
| `calculator` | арифметика, рекурсивный спуск |
| `RunSkillCommand` | запуск bash/python/node/process-скрипта из **установленного и провалидированного** скилл-бандла, процесс заперт директорией бандла |

Плюс любые инструменты с подключённых MCP-серверов (`server.tool` namespace) и скиллы, отобранные LLM-селектором для текущего сообщения.

## Деплой на целевое устройство

Полная инструкция — в [DEPLOY.md](DEPLOY.md): архитектура устройства, сборка, adb, layout `/data/souz-agent/`, monit + init.d, Telegram-бот, быстрый цикл обновления, диагностика.

## Состояние на диске

```
~/.local/state/souz-go/
  sessions/          ← один JSON-файл на chatId
  skills/            ← установленные скилл-бандлы
  skill-validations/ ← кэш вердиктов валидации
  codex-token.json    ← OAuth-токен Codex-провайдера (если используется)
  config.yaml         ← конфиг по умолчанию (переопределяется флагом -config)
```

## Ограничения по конструкции

- **Zero CGO** — `CGO_ENABLED=0` во всех сборках, только чистые Go-библиотеки.
- Нет PostgreSQL, Docker-песочницы, voice API, GigaChat.
- `AgentContext` передаётся по значению через узлы графа — указатель на него внутри узла брать нельзя.
- Каналы не импортируют `pkg/agent`; связка bus ↔ agent живёт только в `cmd/souz-agent/`.
- Целевой размер бинарника: ≤20 MB без сжатия, ≤12 MB с UPX.

Подробный план по фазам — в [docs/plan.md](docs/plan.md).
