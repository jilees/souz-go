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
  enabled: false
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

- **`pkg/channels/sberboom/`** — заглушка (Фаза 2 в `docs/plan.md` не завершена): нет реального WebSocket-клиента к Sber OS bridge, `Start()`/`Send()` ничего не делают. `wiring.go` его вообще не подключает. Голосовой канал колонки как был на picoclaw, так и остаётся — souz-agent пока доступен только по HTTP API.
- **`/v1/me/settings`, `/v1/me/provider-keys`** — не реализованы, см. выше.
- **Логи не персистятся** при запуске через monit, см. раздел "Логи".
