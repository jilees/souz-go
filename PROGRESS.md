# Progress: souz-go

Сводка по фазам из `docs/plan.md`. Актуально на конец Фазы 5 — Фазы 1–5 реализованы и протестированы; из исходного плана осталось только `pkg/channels/sberboom/` и `pkg/channels/mattermost/`.

Статус верификации на конец каждой описанной здесь сессии: `go build ./...`, `go vet ./...`, `gofmt -l .` и `go test -race ./...` — чисто по всем пакетам, с нуля (без кэша тестов).

---

## Фаза 1 — Ядро

- `pkg/graph/` — типизированный граф-движок: `Node`, `Definition`, `Runner` с retry-политикой, итеративная очередь `frame{node, input, depth}` вместо рекурсии, отмена через `context.Done()`.
- `pkg/providers/types.go` — интерфейс `LLMProvider` (`Chat` + `ChatStream(onChunk)`) и DTO (`ChatRequest`/`ChatResponse`/`Message`/`ToolCall`/`ToolDefinition`).
- `pkg/agent/context.go` — `AgentContext` (value semantics, каждый узел графа возвращает новую копию), `AgentSettings`, `EventSink`.
- `pkg/tools/tool.go` — интерфейс `Tool`.
- `pkg/bus/` — `MessageBus`: буферизованные `chan InboundMessage`/`chan OutboundMessage`.
- `pkg/channels/channel.go` — интерфейс `Channel` + `BaseChannel` (allow-list, публикация в шину).
- `pkg/providers/anthropic/` — клиент Anthropic Messages API, SSE-стриминг, ручной парсер `content_block_delta`/`input_json_delta` для сборки tool-calls на лету.
- `pkg/providers/openai_compat/` — клиент OpenAI-совместимого Chat Completions API (OpenAI/Qwen/AiTunnel/Codex через один и тот же клиент, `BaseURL` — параметр), SSE-стриминг.
- `pkg/agent/nodes/llm.go` — узел вызова LLM со стримингом дельт в `EventSink`.
- Smoke-тесты провайдеров — реальные HTTP-запросы к Anthropic/OpenAI, `t.Skip` без API-ключа в окружении.

## Фаза 2 — Каналы и граф-исполнение

- `pkg/channels/channel.go` — тесты на `BaseChannel` (allow-list, wildcard, фильтрация).
- `pkg/channels/telegram/` — long-polling бот поверх `net/http` напрямую (без SDK): `getUpdates`/`sendMessage`, backoff на сетевых ошибках, `Config.BaseURL` для тестов через `httptest`.
- `pkg/agent/nodes/` — узлы `classify` (изначально заглушка-passthrough), `enrich` (инъекция `<context>`-блока с датой/временем, добавление текущего ввода в историю), `toolloop` (выполнение tool-calls, петля обратно к `llm`), `summarize` (сжатие истории при приближении к `ContextSize`, эвристика chars/4).
- `pkg/agent/executor.go` + `pkg/agent/nodes.BuildGraph(...)` — граф `classify → enrich → llm → [toolloop → llm]* → summarize`. Архитектурное решение: сборка графа живёт в `pkg/agent/nodes`, а не в `pkg/agent`, из-за цикла импорта (`pkg/agent/nodes` уже импортирует `pkg/agent`).

## Фаза 3 — Инструменты + MCP

- **Фикс**: `tools.ToDefinition` раньше возвращал неэкспортируемый тип с неэкспортируемыми полями — не собирался за пределами пакета, тулы физически не могли попасть в LLM-запрос. Добавлены `ToDefinitions`, `NewRegistry`, `ArgString/ArgInt/ArgBool`.
- `classify` подключён к реестру тулов — реально прокидывает `ActiveTools` в LLM-запрос (маршрутизация/фильтрация по категориям пока не реализована, ждёт каталога MCP/skills).
- `pkg/tools/math/` — калькулятор, рекурсивный спуск без внешних библиотек: `+ - * / ^`, скобки, унарный минус, `sqrt/sin/cos/tan`.
- `pkg/tools/files/` — `ReadFile`/`ListFiles`/`WriteFile`/`SearchFiles`, sandbox с защитой от `..`-traversal и symlink-эскейпа (рекурсивное резолвение до ближайшего существующего предка).
- `pkg/tools/web/` — `Fetch` (HTML→текст без внешнего парсера, regex-based) и `Search` (скрейпинг DuckDuckGo `/html/`, без платного API). LLM-driven "research"-тул из Kotlin **не портирован**: агент и так может делать многошаговый research через `toolloop`.
- `pkg/mcp/` — клиент Model Context Protocol: JSON-RPC 2.0 (`initialize`/`tools/list`/`tools/call`) поверх `StdioTransport` (subprocess, newline-delimited JSON) и `HTTPSSETransport` (GET открывает SSE-поток, сервер шлёт `event: endpoint`, дальше POST на этот адрес, ответы приходят асинхронно через SSE).

## Фаза 4 — Скиллы

- `pkg/skills/bundle/` — парсер `SKILL.md` (YAML-подобный frontmatter: `name`/`description`/`author`/`version`/`metadata`), нормализация путей, лимиты (64 файла/128KB на файл/512KB на бандл, whitelist расширений), SHA-256 хэш содержимого (не зависит от порядка файлов).
- `pkg/skills/validation/` — пайплайн structural → static (7 regex-правил: prompt-injection, эксфильтрация credentials, приватные ключи, дамп env, деструктивные команды, сетевая эксфильтрация, base64-обфускация) → LLM-валидация. LLM-стадия **всегда fail-closed**: провайдер упал или ответ не распарсился → `REJECT`, а не Go-ошибка. Кэш вердиктов на диске `{skillId}/policies/{version}/{hash}.json`.
- `pkg/skills/registry/` — файловый реестр: managed-скиллы (content-addressed `bundles/{hash}/`, atomic install) + auto-discovery "loose"-скиллов (голая директория с `SKILL.md`, без install-шага).
- `pkg/skills/selection/` — LLM-селектор релевантных скиллов по сообщению пользователя; неизвестные id молча отбрасываются; fail-closed = пустой выбор.
- `pkg/tools/skills/` — `RunSkillCommand` (bash/python/node/process), без Docker (excluded по CLAUDE.md). Таймаут — через process-group kill + `cmd.WaitDelay`, потому что убийство только прямого дочернего процесса оставляло осиротевшие внуки (`sleep 5` под `bash`) держать stdout pipe открытым — поймано тестом, изначальная реализация таймаута не работала.
- `pkg/agent/nodes/skills.go` — узел активации: LLM-выбор → (ре)валидация при `STALE`/отсутствии кэша → инъекция инструкций в `SystemPrompt`. Fail-open на любой ошибке.
- `pkg/agent/nodes/mcp.go` — узел, каждый ход опрашивает уже подключённые MCP-клиенты (`ListTools`) и добавляет тулы в `ActiveTools` с неймспейсом `server.tool`; сломанный клиент пропускается, не валит ход.
- **Существенное упрощение относительно Kotlin**: `RunSkillCommand` авторизует по факту "APPROVED-запись в реестре валидации для текущего hash бандла", а не по "выбран LLM-селектором именно в этом ходу" — строгая послойность пакетов (`pkg/agent` не может импортировать `pkg/tools`) не позволяет протащить per-turn список активных скиллов через `AgentContext` в тул. Граница безопасности (невалидированный скилл никогда не выполнится) при этом сохраняется полностью.
- По той же причине `toolloop` принимает `map[string]*mcp.Client` отдельным параметром и матчит `serverName.toolName` как fallback, когда имени нет в статическом registry.
- Граф теперь `classify → mcp → skills → enrich → llm → [toolloop → llm]* → summarize`, как задокументировано в CLAUDE.md.

## Фаза 5 — Интеграция

- `pkg/config/` — YAML-конфиг через `gopkg.in/yaml.v3` (первая и единственная внешняя зависимость проекта: для полноценного вложенного YAML ручной парсер уже непропорционален, в отличие от плоского frontmatter скиллов). `SecureString` — обёртка для редакции секретов в логах (`String()`/`GoString()` → `[REDACTED]`), **не** шифрование на диске: Kotlin-оригинал шифрует AES-256-GCM+PBKDF2 под multi-tenant сервер; у нас локальный процесс одного пользователя, `config.yaml` и так под 0600 (enforced в `config.Save`).
- `pkg/storage/` — атомарное JSON-хранилище сессий, один файл на `chatId` (temp+rename).
- `pkg/http/` — сокращённый `/v1/**` набор + настоящий SSE (`text/event-stream` через `net/http`+`http.Flusher`, без WebSocket-библиотеки). **Не** route-for-route порт `BackendHttpRoutes.kt`/`BackendV1Dtos.kt`:
  - Kotlin-бэкенд — multi-tenant сервер (Postgres/memory storage modes, onboarding wizard, HTTP-managed settings/provider-keys, Telegram-bot-binding routes, choice/option subsystem), ничего из этого не подходит однопользовательскому embedded-агенту на 256MB RAM;
  - проверено по исходникам: **KMP Compose desktop-клиент вообще не вызывает этот HTTP API** — гоняет агента in-process, живого потребителя для байтовой совместимости не существует;
  - реализовано: `GET /health`, `GET/POST /v1/chats`, `GET/POST /v1/chats/{chatId}/messages`, `GET /v1/chats/{chatId}/events` (SSE), `POST /v1/chats/{chatId}/cancel-active`; сохранены `/v1/**`-неймспейс, camelCase JSON, error envelope `{"error":{"code","message"}}`, словарь событий `message.delta`/`execution.*`/`tool.call.*`;
  - не реализовано сознательно: onboarding, HTTP-managed settings/provider-keys (это всё — `config.yaml`), per-chat Telegram-binding, options/choice-подсистема, archive/unarchive, pagination cursors (в самом Kotlin они и так всегда `null`).
- `cmd/souz-agent/main.go` + `wiring.go` — полная DI-сборка: provider → tools registry → skills registry/validation store → MCP-клиенты → `BuildGraph` → `Executor` → bus → channels → HTTP-сервер → graceful shutdown по SIGINT/SIGTERM.
- **ARM64 кросс-компиляция** — `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w"` собирается чисто, статический ELF-бинарь **7.0 MB** (цель CLAUDE.md — ≤20MB).
- **Smoke-тест реального бинаря** (не только юнит-тесты): собран → запущен с конфигом → `curl /health`, `POST /v1/chats`, `GET /v1/chats` отработали end-to-end → `SIGINT` → чистое завершение, сессия атомарно легла на диск.

---

## Что осталось

- `pkg/channels/sberboom/` — WebSocket-клиент к Sber OS bridge.
- `pkg/channels/mattermost/` — WebSocket real-time.

Обе явно отложены пользователем в рамках Фазы 2 ("давай сначала абстрактный слой и telegram, остальное потом") и с тех пор не поднимались.

## Архитектурные решения (полный список — `docs/plan.md`)

1. Message Bus — буферизованные каналы.
2. Graph engine — итеративная очередь, `context.Done()` вместо coroutine cancellation.
3. `AgentContext` — value semantics, каждый узел возвращает новую копию.
4. `LLMProvider` — `Chat()` + `ChatStream(onChunk)` на всех провайдерах.
5. `Tool` — `Name/Description/Schema/Execute`, `Schema()` как `json.RawMessage`.
6. HTTP-совместимость — узнаваемая форма `/v1/**`, не байтовый порт (см. Фазу 5 выше).
7. Single-user — `userId = "default"` везде, auth-заголовки игнорируются.
8. Zero CGO — `CGO_ENABLED=0` во всех командах сборки; единственная внешняя зависимость (`yaml.v3`) — чистый Go.
9. Сборка графа в `pkg/agent/nodes.BuildGraph(...)`, а не в `pkg/agent` — из-за цикла импорта.
10. `classify` — маршрутизация по категориям инструментов не реализована (в Kotlin это LLM-классификатор), узел сейчас просто прокидывает весь реестр тулов в `ActiveTools`.
