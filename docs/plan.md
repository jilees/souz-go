# Plan: Go-порт souz agent для embedded-устройств

Скопировано из сессии планирования. Актуальная версия в плане Claude Code.

## Контекст

Проект souz написан на Kotlin Multiplatform и не может работать на SberBoom Home: 256 MB RAM, 30 MB свободного места, ARM64 Linux — JVM в эти ограничения не помещается. Требуется Go-порт серверного агентного ядра.

Целевые каналы: SberBoom (WebSocket к Sber OS bridge), Telegram, Mattermost.
Исключены: GigaChat, voice API, HTTP multi-user backend, PostgreSQL, Docker sandbox, desktop-инструменты.

HTTP API `/v1/**` совместим с KMP Compose клиентом souz.

## Фазы

### Фаза 1 — Ядро (✅ начата)
- [x] go.mod, структура директорий
- [x] pkg/graph/ — граф-движок (Node, Definition, Runner с retry)
- [x] pkg/providers/types.go — LLMProvider interface + DTO
- [x] pkg/agent/context.go — AgentContext, AgentSettings, EventSink
- [x] pkg/tools/tool.go — Tool interface
- [x] pkg/bus/ — MessageBus
- [x] pkg/channels/channel.go — Channel interface + BaseChannel
- [x] pkg/providers/anthropic/ — Anthropic Chat API клиент (Messages API, SSE streaming)
- [x] pkg/providers/openai_compat/ — OpenAI-compatible клиент (Chat Completions API, SSE streaming)
- [x] pkg/agent/nodes/llm.go — LLM-вызов узел со стримингом
- [x] Smoke test: запросить LLM через провайдер вручную (skip без API-ключа в окружении)

### Фаза 2 — Каналы
- [x] pkg/channels/channel.go — Channel interface + BaseChannel (allow-list, bus publish) — тесты добавлены
- [ ] pkg/channels/sberboom/ — WebSocket клиент
- [x] pkg/channels/telegram/ — long-polling bot (Bot API напрямую через net/http, без SDK)
- [ ] pkg/channels/mattermost/ — WebSocket real-time
- [x] pkg/agent/nodes/ — classify (placeholder), enrich, toolloop, summarize
- [x] pkg/agent/executor.go + основной цикл агента (граф собирается в pkg/agent/nodes.BuildGraph)

### Фаза 3 — Инструменты + MCP
- [x] pkg/tools/tool.go — ToDefinition/ToDefinitions/NewRegistry/Arg{String,Int,Bool} (ToDefinition раньше возвращал неэкспортируемый тип — не собирался вне пакета; исправлено)
- [x] pkg/tools/math/ — Calculator (рекурсивный спуск, без внешних зависимостей)
- [x] pkg/tools/files/ — ReadFile/ListFiles/WriteFile/SearchFiles, sandbox с защитой от `..`/симлинк-эскейпа
- [x] pkg/tools/web/ — Fetch (HTML→текст) + Search (скрейпинг DuckDuckGo HTML, без API-ключа); LLM-driven "research"-тул из Kotlin сознательно не портирован (агент сам делает многошаговый research через toolloop)
- [x] pkg/mcp/ — JSON-RPC 2.0 клиент (Initialize/ListTools/CallTool) + StdioTransport + HTTPSSETransport; узел интеграции в граф (nodes/mcp.go) — Фаза 4
- [x] classify теперь реально прокидывает локальные тулы в ActiveTools (было: полная заглушка) — маршрутизация/фильтрация по-прежнему отложена до каталога MCP/skills

### Фаза 4 — Скиллы
- [x] pkg/skills/bundle/ — парсер SKILL.md (name/description/author/version/metadata), нормализация путей, лимиты (64 файла/128KB/512KB, whitelist расширений), контент-хэш (SHA-256, независим от порядка файлов)
- [x] pkg/skills/validation/ — structural → static (7 regex-правил) → LLM validation; кэш вердиктов на диске `{skillId}/policies/{v}/{hash}.json`; LLM-стадия всегда fail-closed (провайдер упал / ответ не распарсился → REJECT, не Go-ошибка)
- [x] pkg/skills/registry/ — файловый реестр: managed-скиллы (content-addressed `bundles/{hash}/`, atomic install) + "loose"-скиллы (голая директория с SKILL.md, авто-обнаружение, без install-шага)
- [x] pkg/skills/selection/ — LLM-селектор релевантных скиллов по сообщению пользователя; неизвестные id молча отбрасываются; fail-closed = пустой выбор
- [x] pkg/tools/skills/ — RunSkillCommand (bash/python/node/process), рабочая директория заперта в bundle root, capped output, таймаут через process-group kill + WaitDelay (иначе осиротевшие дочерние процессы держат pipe открытым — поймано тестом на `sleep 5`)
- [x] pkg/agent/nodes/skills.go — узел активации: выбор → (ре)валидация при STALE/отсутствии записи → инъекция инструкций в SystemPrompt (не в history[0], в отличие от Kotlin-квирка); fail-open на любой ошибке
- [x] pkg/agent/nodes/mcp.go — узел, каждый ход опрашивает уже подключённые MCP-клиенты (ListTools) и добавляет тулы в ActiveTools с неймспейсом `server.tool`; сломанный клиент пропускается, не валит ход
- [x] **Существенное упрощение относительно Kotlin**: `RunSkillCommand` авторизует по факту "APPROVED-запись в реестре валидации для текущего hash бандла", а не по "выбран LLM-селектором в этом ходу". Из-за строгой послойности пакетов (`pkg/agent` не может импортировать `pkg/tools`, см. решение #9) протащить per-turn "разрешённый набор скиллов" через AgentContext в тул нельзя не сломав слои — а глобальная APPROVED-проверка сохраняет реальную границу безопасности (невалидированный/отклонённый скилл никогда не выполнится), просто без доп. ограничения "только то, что выбрано в этом ходу"
- [x] По той же причине `toolloop` теперь принимает `map[string]*mcp.Client` отдельным параметром и матчит `serverName.toolName` как fallback, когда имя не найдено в статическом registry — MCP-тулы динамические (сервер может менять каталог), а registry статичен (собирается один раз в BuildGraph)

### Фаза 5 — Интеграция (✅ завершена)
- [x] pkg/config/ — YAML config (gopkg.in/yaml.v3 — первая и единственная внешняя зависимость проекта, оправдана: полноценный вложенный YAML, ручной парсер непропорционален в отличие от плоского frontmatter скиллов) + SecureString
- [x] pkg/storage/ — атомарное JSON-хранилище сессий, один файл на chatId (temp+rename, как everywhere else в проекте)
- [x] pkg/http/ — /v1/** routes (сокращённый набор) + SSE event sink (net/http стримингом, без WebSocket-библиотеки)
- [x] cmd/souz-agent/main.go — полный DI: provider → tools registry → skills registry/validation store → MCP-клиенты → BuildGraph → Executor → bus → channels → HTTP-сервер → graceful shutdown по SIGINT/SIGTERM
- [x] ARM64 cross-compile: `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w"` — чисто, статический ELF-бинарь **7.0 MB** (цель ≤20MB из CLAUDE.md)
- [x] Smoke test реального бинаря: `go build` → запуск с конфигом → `curl /health`, `POST /v1/chats`, `GET /v1/chats` работают end-to-end → `SIGINT` → чистое завершение, сессия сохранена атомарно на диск

**Существенное упрощение HTTP API относительно Kotlin-спеки** (задокументировано в package doc `pkg/http/http.go`): реализован не route-for-route порт `BackendHttpRoutes.kt`/`BackendV1Dtos.kt`, а урезанный набор со схожей формой. Причины:
- Kotlin-бэкенд — multi-tenant сервер (Postgres/memory storage modes, onboarding wizard, HTTP-managed settings/provider-keys, Telegram-bot-binding routes, choice/option subsystem) — ничего из этого не подходит однопользовательскому embedded-агенту на 256MB RAM
- Проверено по исходникам: **KMP Compose desktop-клиент вообще не вызывает этот HTTP API** — гоняет агента in-process. Живого потребителя, которому нужна байтовая совместимость, не существует
- Реализовано: `GET /health`, `GET/POST /v1/chats`, `GET/POST /v1/chats/{chatId}/messages`, `GET /v1/chats/{chatId}/events` (SSE), `POST /v1/chats/{chatId}/cancel-active`. Сохранены: `/v1/**`-неймспейс, camelCase JSON-поля, error envelope `{"error":{"code","message"}}`, словарь событий `message.delta`/`execution.started`/`execution.finished`/`execution.failed`/`execution.cancelled`/`tool.call.*`
- Не реализовано (сознательно): `/v1/onboarding/**`, `/v1/me/settings`, `/v1/me/provider-keys` (это всё — config.yaml на однопользовательском embedded-агенте), `/v1/chats/{chatId}/telegram-bot` (Telegram настраивается через config.yaml, не per-chat HTTP), `/v1/options/**` (choice/option-фича не реализована в графе), title/archive/unarchive для чатов, pagination cursors (в самом Kotlin они всё равно всегда `null` — это уже заглушка в оригинале)
- **Транспорт живых событий — настоящий SSE** (`text/event-stream`, `net/http` + `http.Flusher`), не WebSocket, хотя Kotlin-оригинал использует WS+polling. CLAUDE.md и docs/plan.md сами называют это "SSE event sink" — решение уже задокументировано в проекте, я его не изобретал, а честно реализовал буквально (SSE не требует WS-библиотеки, меньше кода, меньше веса бинаря)
- `EventSink.EmitError`/`.Done()` (были объявлены в интерфейсе, но ни один узел графа их не вызывал) теперь используются по назначению: HTTP-хендлер сам вызывает их вокруг `Executor.Execute` для событий `execution.failed`/`execution.finished` — это уровень целого хода, а не отдельного узла

**SecureString — только редакция в логах, не шифрование на диске** (задокументировано в package doc `pkg/config/config.go`): Kotlin-оригинал шифрует ключи AES-256-GCM с PBKDF2, потому что это multi-tenant сервер, хранящий секреты многих пользователей. souz-go — локальный процесс одного пользователя; config.yaml и так под той же защитой, что и любой другой локальный секрет-файл (0600, enforced в `config.Save`). Полноценное шифрование добавило бы жизненный цикл master-key без соответствующей угрозы на этом target.

## Архитектурные решения

1. **Message Bus**: буферизованные каналы (chan InboundMessage / chan OutboundMessage)
2. **Graph engine**: итеративная очередь frame{node, input, depth}, context.Done() вместо coroutine cancellation
3. **AgentContext**: value semantics, каждый узел возвращает новую копию
4. **LLMProvider**: Chat() + ChatStream(onChunk) — два метода на все провайдеры
5. **Tool**: Name/Description/Schema/Execute — Schema() как json.RawMessage
6. **HTTP compatibility**: /v1/** = BackendHttpRoutes.kt, SSE events = BackendV1Dtos.kt
7. **Single-user**: userId = "default" везде, auth headers игнорируются
8. **Zero CGO**: CGO_ENABLED=0 во всех командах сборки
9. **Graph assembly living in pkg/agent/nodes**: `pkg/agent/nodes` уже импортирует `pkg/agent` (для `AgentContext`), поэтому `pkg/agent` не может импортировать `pkg/agent/nodes` обратно (цикл). Поэтому сборка полного графа (`classify → mcp → skills → enrich → llm → [toolloop → llm]* → summarize`, порядок из CLAUDE.md) живёт в `nodes.BuildGraph(...)`, а `agent.Executor`/`agent.NewExecutor` остаются агностичными к конкретным узлам — принимают уже готовые `*graph.Definition`/`*graph.Node`. Вызывающий код (cmd/souz-agent, Фаза 5) импортирует оба пакета и связывает их.
10. **classify — заглушка**: реальная маршрутизация по категориям инструментов (как в Kotlin `NodesClassification`) требует каталога тулов из Фазы 3/4 (pkg/tools, pkg/mcp, pkg/skills). Пока узел pass-through, форма графа зафиксирована для последующего наполнения.
