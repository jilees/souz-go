# Деплой souz-agent на SberBoom Home

Устройство: SberBoom Home — Android-based ARM64 Linux (`aarch64`), 256 MB RAM, 30 MB свободного места.
Управление сервисами: monit + BusyBox init (`/etc/init.d/S*`).

souz-agent разворачивается **параллельно с picoclaw**, не заменяя его — оба сервиса управляются monit'ом независимо.

---

## Архитектура целевой платформы

| Параметр | Значение |
|---|---|
| OS | Linux (Android kernel) |
| Arch | `arm64` (`aarch64`) |
| CGO | недоступен — только статические бинарники (`CGO_ENABLED=0`) |
| `/` (system) | overlay, **read-only** без remount |
| `/data/` | read-write всегда, без remount, переживает reboot |

---

## Сборка бинарника

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o build/souz-agent-arm64 ./cmd/souz-agent
```

Текущий размер: **~7.1 MB** (лимит из CLAUDE.md — ≤20 MB uncompressed).

---

## ADB-подключение

```bash
adb connect 192.168.2.105:5555
adb devices   # ожидаем: 192.168.2.105:5555  device
```

Если статус `offline`: `adb disconnect 192.168.2.105:5555 && adb connect 192.168.2.105:5555`.
Не помогает — физически перезагрузить колонку, повторить connect.

---

## Layout на устройстве

```
/data/souz-agent/
  souz-agent          ← бинарник (0755)
  config.yaml          ← конфиг, включая API-ключи (0600)
  souz-agent.pid        ← pidfile, создаёт start-stop-daemon
  state/                ← DataDir агента: sessions/, skills/, skill-validations/, codex-token.json

/etc/init.d/S91souz-agent   ← init-скрипт (0755)
/etc/monit.d/souz-agent.cfg ← monit-конфиг (0600)
```

Бинарник и конфиг живут в `/data/` целиком — только `S91souz-agent` и `souz-agent.cfg` требуют read-only системного раздела. Поэтому **обновление бинарника/конфига не требует remount+reboot**, а вот первичная установка init-скрипта и monit-конфига требует один раз.

### config.yaml (пример)

```yaml
dataDir: /data/souz-agent/state

provider: openai_compat   # | anthropic | codex
model: deepseek/deepseek-v4-pro
temperature: 0.7
contextSize: 16000
maxTokens: 4096

openaiCompat:
  apiKey: sk-or-v1-...
  baseURL: https://openrouter.ai/api/v1

tools:
  webEnabled: true
  filesRoot: ""

http:
  enabled: true
  addr: ":8080"

skills:
  enabled: true

telegram:
  enabled: true
  token: "<bot-token-from-BotFather>"
  allowFrom:
    - "<your-telegram-user-id>"
```

Провайдеры:
- **`anthropic`** / **`openai_compat`** — статичный API-ключ в `config.yaml` (`SecureString`, редактируется вручную через adb pull/push).
- **`codex`** — OAuth device-flow к ChatGPT-подписке, ключ не нужен. Логин выполняется отдельно (см. ниже), токен пишется в `{dataDir}/codex-token.json`, не в config.yaml.

### /etc/init.d/S91souz-agent

```sh
#!/bin/sh
NAME=souz-agent
BIN_FILE=/data/souz-agent/souz-agent
CONFIG=/data/souz-agent/config.yaml
PIDFILE=/var/run/"$NAME".pid

start() {
    start-stop-daemon -S -b -m -p "${PIDFILE}" -x "${BIN_FILE}" -- -config "${CONFIG}"
}

stop() {
    start-stop-daemon -K -p "${PIDFILE}"
}

case "$1" in
    start|stop)
        "$1"
        ;;
    restart)
        stop
        sleep 1
        start
        ;;
    *)
        echo "Usage: $0 {start|stop|restart}"
        exit 1
esac
```

### /etc/monit.d/souz-agent.cfg

```
check process souz-agent with pidfile /var/run/souz-agent.pid
  start program = "/etc/init.d/S91souz-agent start"
  stop  program = "/etc/init.d/S91souz-agent stop"
```

`/etc/monitrc` уже содержит `include /etc/monit.d/*.cfg`, так что новый `.cfg` подхватывается через `monit reload`, без релоада самого демона monit.

---

## Первичная установка (один раз)

```bash
# 1. Данные — без reboot
adb shell "mkdir -p /data/souz-agent/state"
adb push build/souz-agent-arm64 /data/souz-agent/souz-agent
adb push build/config.yaml /data/souz-agent/config.yaml
adb shell "chmod 755 /data/souz-agent/souz-agent && chmod 600 /data/souz-agent/config.yaml"

# 2. Remount + reboot — только для /etc/init.d и /etc/monit.d
adb remount
adb reboot
adb wait-for-device
# дать устройству реально догрузиться, iначе следующий remount ловит гонку:
adb shell "cat /proc/uptime"   # подождать, пока не станет расти стабильно (~1-2 мин)
adb remount

# 3. Init-скрипт и monit-конфиг
adb push build/S91souz-agent /etc/init.d/S91souz-agent
adb shell "chmod 755 /etc/init.d/S91souz-agent"
adb push build/souz-agent.cfg /etc/monit.d/souz-agent.cfg
adb shell "chmod 600 /etc/monit.d/souz-agent.cfg"

# 4. Поднять
adb shell "monit reload"
adb shell "monit start souz-agent"
adb shell "monit status souz-agent"
```

Ребут задевает весь девайс, включая picoclaw — он поднимается сам через monit (`on reboot: start`), но будет короткий даунтайм голосового ассистента. Планировать не в момент активного использования колонки.

---

## Codex-логин (провайдер `codex`)

```bash
adb shell "/data/souz-agent/souz-agent -config /data/souz-agent/config.yaml -codex-login"
```

Печатает ссылку `https://auth.openai.com/codex/device` и код — авторизоваться нужно с другого устройства (телефон/ноутбук) за отведённое время. Таймаут ожидания фиксированный и не увеличивается — если не успел, просто перезапустить команду. Успешный логин пишет токен в `{dataDir}/codex-token.json`; сам сервис (через monit) эту команду не запускает — только обычный `-config` без `-codex-login`.

---

## Telegram-канал

`pkg/channels/telegram/` — long-polling бот (напрямую через `net/http`, без SDK), в отличие от `sberboom`-канала полностью реализован и подключается в `wiring.go`/`buildChannels`.

1. Создать бота через [@BotFather](https://t.me/BotFather) → `/newbot`, получить токен вида `123456:AA...`.
2. **`allowFrom` обязателен для личного использования** — пустой список = бот отвечает всем, кто его найдёт, расходуя API-ключ провайдера. Свой numeric user ID можно узнать у `@userinfobot`, либо (если на устройстве уже стоит picoclaw с настроенным Telegram) вытащить из его конфига:
   ```bash
   adb shell "cat /data/picoclaw/config.json" | python3 -c "
   import json,sys
   print(json.load(sys.stdin)['channels']['telegram']['allow_from'])
   "
   ```
3. Прописать `token`/`allowFrom` в `config.yaml` (см. пример выше), запушить, `monit restart souz-agent`.
4. Написать боту в Telegram и проверить, что дошло именно до souz-agent (не до другого процесса на устройстве) — через сессионное хранилище, у Telegram-чатов `chatId` имеет вид `telegram:<userId>`:
   ```bash
   adb forward tcp:8080 tcp:8080
   curl -s http://localhost:8080/v1/chats | python3 -m json.tool
   curl -s "http://localhost:8080/v1/chats/telegram%3A<userId>/messages" | python3 -m json.tool
   ```

Токен бота и `config.yaml` целиком — секрет уровня API-ключа: `config.yaml` на диске стоит `0600`, но сам файл никогда не должен попадать в git (см. `.gitignore` → `/build/`).

---

## Скиллы

Скилл — директория с `SKILL.md` (YAML-frontmatter `name`/`description` + markdown-инструкции), опционально с дополнительными файлами. Устанавливается двумя способами:

- **Loose-скилл** — просто положить директорию в `{dataDir}/skills/{skillId}/SKILL.md` на устройстве. Реестр читает каталог заново при каждом ходе (`os.ReadDir`), рестарт сервиса не нужен.
- Через `registry.SaveSkillBundle` (content-addressed `bundles/{hash}/`) — актуально для программной установки, для ручного деплоя проще loose.

```bash
adb shell "mkdir -p /data/souz-agent/state/skills/<skillId>"
adb push SKILL.md /data/souz-agent/state/skills/<skillId>/SKILL.md
```

### Валидация

Активация скилла требует прохождения пайплайна **structural → static (regex) → LLM-проверка** — он запускается **автоматически**, при первом сообщении, которое LLM-селектор сочтёт релевантным описанию скилла (`description` в frontmatter). Скилл не подключается к системному промпту, пока не получит `APPROVED`.

Результат кэшируется на диске:
```
{dataDir}/skill-validations/{skillId}/policies/{policyVersion}/{bundleHash}.json
```

Посмотреть вердикт:
```bash
adb shell "find /data/souz-agent/state/skill-validations -type f -exec cat {} \;" | python3 -m json.tool
```

Поле `status`: `APPROVED` / `REJECTED` (+ `reasons`/`findings` с объяснением). Пока хэш содержимого `SKILL.md` не изменился и запись `APPROVED` — повторная валидация не идёт. Форсировать перевалидацию (например, после правки промпта):
```bash
adb shell "rm /data/souz-agent/state/skill-validations/{skillId}/policies/1/{bundleHash}.json"
```
— на следующем подходящем сообщении пайплайн прогонится заново.

**Важно (исправленный баг):** до текущей версии `selection.Select` и `validation.ValidateWithLLM` не передавали в `ChatRequest` сконфигурированную модель (`config.yaml`'s `model`) — из-за этого запрос уходил на захардкоженный фолбэк `openai_compat.defaultModel = "gpt-5-nano"` вместо реальной модели пользователя. Для reasoning-моделей это означало, что весь `maxTokens`-бюджет валидатора сжирался на скрытый reasoning, ответ обрывался до JSON-вердикта, и **любой** скилл получал fail-closed `REJECTED` независимо от содержимого. Пофикшено — `nodes.SkillsConfig.Model` (= `cfg.Model` из `config.yaml`) теперь пробрасывается в оба вызова.

### Ограничения рантайма на SberBoom

На устройстве **нет `bash`** (только BusyBox `sh`) и **нет `curl`** в `PATH` агента (только `wget` от BusyBox). Для `RunSkillCommand`:
- `runtime: "bash"` теперь **автоматически падает на `sh`**, если `bash` не найден в `PATH` (`pkg/tools/skills/exec.go`, `bashBinary()` — тот же паттерн, что уже был у `pythonBinary()` для `python3`/`python`). На SberBoom это значит: скрипт реально исполнится через BusyBox `sh`, а не свалится с `"bash" is not available on this system"`.
- **Но** bash-специфичный синтаксис (массивы, `[[ ]]`, process substitution `<(...)`, `local`-only фичи и т.п.) под `sh` не заработает — фолбэк не эмулирует bash, только не даёт упасть на старте. Пишите скрипты в POSIX sh, если runtime может исполняться на embedded-таргете.
- Для простых HTTP-вызовов (как в tv-control) проще и надёжнее `runtime: "process"` с прямым `argv` (например `["wget","-q","-O","-","--header","Content-Type: application/json","--post-data","{...}","http://..."]`) — без шелла вообще, что заодно снимает проблему экранирования кавычек в JSON, который LLM подставляет в команду.

---

## Управление сервисом

```bash
adb shell monit status souz-agent
adb shell monit start souz-agent
adb shell monit stop souz-agent
adb shell monit restart souz-agent
```

### Зависший PID-файл

Если после restart сервис не поднимается из-за `already running`:
```bash
adb shell "cat /data/souz-agent/souz-agent.pid"   # или /var/run/souz-agent.pid
adb shell "kill <PID> 2>/dev/null; rm -f /var/run/souz-agent.pid"
adb shell monit start souz-agent
```

---

## Логи

**Известное ограничение**: `start-stop-daemon` сейчас не редиректит stdout/stderr — весь `slog`-вывод агента никуда не пишется на диске при запуске через monit. Для диагностики после падения сервиса либо:
- временно гонять бинарник вручную в foreground (`adb shell` без backgrounding), либо
- добавить редирект в `S91souz-agent`: `... -x "${BIN_FILE}" -- -config "${CONFIG}" >> /data/souz-agent/state/agent.log 2>&1` (не сделано намеренно, чтобы не проектировать ротацию логов заранее — сделать, когда реально понадобится).

---

## Быстрый цикл обновления (бинарник и/или конфиг, без reboot)

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o build/souz-agent-arm64 ./cmd/souz-agent
adb shell monit stop souz-agent
adb push build/souz-agent-arm64 /data/souz-agent/souz-agent
adb shell "chmod 755 /data/souz-agent/souz-agent"
adb shell monit start souz-agent
```

Только конфиг:
```bash
adb pull /data/souz-agent/config.yaml build/config.yaml
# редактируем build/config.yaml
adb push build/config.yaml /data/souz-agent/config.yaml
adb shell monit restart souz-agent
```

---

## HTTP API (подключение клиента)

Сервер слушает на всех интерфейсах (`addr: ":8080"` в config.yaml) — доступен напрямую по локальной сети без `adb forward`.

- **Host**: `192.168.2.105` (актуальный IP колонки в вашей сети)
- **Port**: `8080`
- **Base URL**: `http://192.168.2.105:8080`

| Метод | Путь | Назначение |
|---|---|---|
| GET | `/health` | health-check |
| GET | `/v1/chats` | список чатов |
| POST | `/v1/chats` | создать чат (`{}` или `{"title":"..."}`) |
| GET | `/v1/chats/{chatId}/messages` | история сообщений |
| POST | `/v1/chats/{chatId}/messages` | отправить сообщение — тело `{"content":"..."}` (не `text`) |
| GET | `/v1/chats/{chatId}/events` | SSE-поток событий выполнения |
| POST | `/v1/chats/{chatId}/cancel-active` | отменить текущее выполнение |

Auth-заголовки принимаются, но игнорируются (single-user, `userId="default"`).
`/v1/me/settings` и `/v1/me/provider-keys` **не реализованы** — смена модели/провайдера только через правку `config.yaml` + `monit restart` (см. выше).

Доступ только в пределах локальной Wi-Fi сети — порт наружу не пробрасывать, аутентификации нет.

---

## Диагностика подключения adb

```bash
adb shell "mount | grep -E ' / '"                          # overlay ro/rw статус
adb shell "touch /etc/monit.d/.test && echo RW || echo RO"  # быстрый тест на запись
adb shell "uname -m; cat /proc/version"                     # arch/kernel sanity
```

---

## Известные ограничения текущей версии souz-agent

- **`pkg/channels/sberboom/`** — заглушка (Фаза 2 в `docs/plan.md` не завершена): нет реального WebSocket-клиента к Sber OS bridge, `Start()`/`Send()` ничего не делают. `wiring.go` его вообще не подключает. Голосовой канал колонки как был на picoclaw, так и остаётся — souz-agent сейчас доступен через Telegram-бота и HTTP API, не через голос.
- **`/v1/me/settings`, `/v1/me/provider-keys`** — не реализованы, см. выше. Смена модели/провайдера/Telegram-токена — только через `config.yaml` + `monit restart`.
- **Логи не персистятся** при запуске через monit, см. раздел "Логи".
