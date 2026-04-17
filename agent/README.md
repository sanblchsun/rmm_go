# RMM Agent

Удалённый агент для захвата экрана и управления.

## Требования

- **Агент (Windows):**
  - [FFmpeg](https://ffmpeg.org/download.html) в PATH
  - [Go 1.24+](https://go.dev/dl/) для сборки
  - MinGW-w64 (для сборки с CGO)

- **Сервер:**
  - Python 3.10+
  - `pip install -r requirements.txt`

- **Клиент:**
  - Современный браузер (Chrome, Firefox, Edge)

## Сборка

### Вариант 1: Кросс-компиляция в Linux

```bash
cd agent
CC=x86_64-w64-mingw32-gcc CGO_ENABLED=1 GOOS=windows GOARCH=amd64 go build -o agent.exe .
```

### Вариант 2: Нативная сборка в Windows

1. Установите [MinGW-w64](https://www.mingw-w64.org/) или через MSYS2:

   ```powershell
   # Установка MSYS2
   winget install msys2.msys2
   # В терминале MSYS2:
   pacman -S mingw-w64-x86_64-gcc
   ```

2. Сборка:
   ```powershell
   $env:CC = "gcc"
   $env:CGO_ENABLED = "1"
   go build -o agent.exe .
   ```

## Запуск

### 1. Сервер (сигналинг)

```bash
python main.py
# или
uvicorn main:app --host 0.0.0.0 --port 8000
```

Сервер слушает на `http://0.0.0.0:8000`

### 2. Агент

Отредактируйте `main.go` строку 29, укажите адрес сервера:

```go
const serverURL = "ws://192.168.88.127:8000/ws/agent/agent1"
```

Запуск:

```bash
./agent.exe
```

### 3. Клиент (браузер)

Откройте в браузере:

```
http://192.168.88.127:8000
```

## Использование

1. Запустите сервер
2. Запустите агент на удалённом ПК
3. Откройте `http://<server-ip>:8000` в браузере
4. Управление мышью и клавиатурой работает автоматически
5. Переключение раскладки: **Ctrl+Shift**

## Команды управления

| Действие   | Описание                                      |
| ---------- | --------------------------------------------- |
| Мышь       | Движение, клики (левая/средняя/правая кнопка) |
| Клавиатура | Все клавиши + русский ввод                    |
| Ctrl+Shift | Переключение EN/RU индикатора                 |

## Устранение проблем

**FFmpeg не найден:** Добавьте FFmpeg в PATH или положите рядом с agent.exe

**Нет видео:** Проверьтеfirewall - порт 8000 должен быть открыт

# **Тормозит:** Уменьшите fps в `main.go` строка 276: `"-framerate", "30"`

### Резюме по каналам:

### WebSocket соединения (FastAPI сервер) - используются ТОЛЬКО для сигнализации WebRTC (обмен SDP/ICE). Через них НЕ передаются рабочие данные.

### Внутри WebRTC создаются ДВА DataChannel:

## для JSON сообщений: команды мыши/клавиатуры, запросы информации

controlChannel (
"control"
)

### для BLOB данных: скриншоты, файлы

binaryChannel (
"binary"
)

### Видеопоток передается через отдельный WebRTC видеотрек, не связанный с DataChannels.

### Создание каналов: Браузер создает DataChannels, Go-агент получает их через

pc.OnDataChannel()

### Отправка/получение:

JSON →
channel.send(JSON.stringify())
/
channel.SendText()

BLOB →
channel.send(ArrayBuffer)
/
channel.Send([]byte)

==============================================================================
В проекте определение координат мыши для рабочего стола и для окна приложения внутри рабочего стола определяются по разному, мне нужен подробно е описание как это происходить, не пиши это в коде, я сохраню это в отдельном файле. Дело в том что есть ошибки при определения координат мыши на краю окна приложения. И что интересно, если обновить браузер, координаты нормализуются.

Архитектура системы координат
В проекте используются четыре различные системы координат:

Браузерные координаты - координаты курсора в окне браузера (clientX, clientY)
Видео элемент координаты - координаты внутри HTML video элемента с учетом масштабирования
Удаленные экранные координаты - физические координаты экрана на удаленной машине
Координаты активного окна - относительные координаты внутри конкретного окна приложения

А можно сделать проще и надежней?

Какой подход выбрать?
Для быстрого исправления - вариант №1 (использовать video размеры)
Для надежности - вариант №2 (относительные координаты)
Для простоты - вариант №5 (убрать все лишнее)
Для стабильности - вариант №6 (фиксированное разрешение)

Во-первых разрешение у агента разное, нужна адаптивная система.
Во-вторых нужен способ который можно будет использовать, если агенте будем переписывать с GO на C++.
В третьих надо ориентироваться на опыт в уже известных, крупных проектах. Для начало реализовать упрощенный вариант, потом развивать.

Код исходного проекта не предоставлется, только описание.

# Подробное описание координатных систем в RMM проекте

## Обзор проблемы

В вашем проекте действительно используется сложная система преобразований координат между несколькими уровнями, что создает потенциальные точки отказа. Основная проблема заключается в том, что **нормализация координат после обновления браузера** указывает на проблемы с состоянием или синхронизацией размеров между компонентами.

## Детальный анализ текущих координатных систем

### 1. Браузерные координаты (clientX, clientY)

- **Источник**: DOM события мыши в браузере
- **Система отсчета**: Левый верхний угол окна браузера (0,0)
- **Особенности**: Включает все элементы интерфейса браузера (toolbar, scrollbar, etc.)
- **Получение**: `event.clientX`, `event.clientY`

### 2. Видео элемент координаты

- **Источник**: HTML `<video>` элемент с CSS стилизацией
- **Система отсчета**: Левый верхний угол video элемента (0,0)
- **Особенности**:
  - Учитывает `object-fit: contain` - видео масштабируется с сохранением пропорций
  - Может иметь черные полосы (letterboxing/pillarboxing)
  - Размеры получаются через `getBoundingClientRect()`
- **Преобразование**: `clientX/Y - rect.left/top`

### 3. Нативное видео разрешение

- **Источник**: Фактический размер видеопотока от FFmpeg
- **Система отсчета**: Пиксели исходного видео (0,0) до (videoWidth, videoHeight)
- **Особенности**:
  - Может отличаться от размеров video элемента в DOM
  - Получается из `video.videoWidth/videoHeight`
  - Соответствует параметрам FFmpeg `-s WxH`

### 4. Физический экран удаленной машины

- **Источник**: Реальное разрешение экрана на Go-агенте
- **Система отсчета**: Пиксели физического дисплея (0,0) до (screenWidth, screenHeight)
- **Получение**:
  - Windows: `GetSystemMetricsForDpi()`
  - Кросс-платформа: `robotgo.GetScreenSize()`
  - FFmpeg детекция через вывод ошибок

### 5. Координаты активного окна приложения

- **Источник**: Windows API `GetWindowRect()`
- **Система отсчета**: Относительно физического экрана
- **Особенности**:
  - Координаты окна: `(window.x, window.y, window.width, window.height)`
  - Для определения курсора изменения размера окна
  - Обновляется периодически (200ms)

## Цепочка преобразований координат

```
Browser Click (clientX, clientY)
         ↓
    [getBoundingClientRect]
         ↓
Video Element coords (xInVideo, yInVideo)
         ↓
    [object-fit: contain calculation]
         ↓
Native Video coords (videoX, videoY)
         ↓
    [scale to remote screen]
         ↓
Remote Screen coords (remoteX, remoteY)
         ↓
    [robotgo coordinate scaling]
         ↓
Physical Robot coords (robotX, robotY)
```

## Рекомендованное упрощение архитектуры

Исходя из ваших требований (адаптивность, переносимость на C++, опыт крупных проектов), рекомендую **подход с единственным источником правды**:

### Упрощенная архитектура v2.0

```
Browser Click → Video Element → Remote Screen → Robot Action
     (DOM)        (Canvas)         (Logical)      (Physical)
```

**Ключевые принципы:**

1. **Видео элемент как единственный источник размеров**
2. **Прямое 1:1 отображение видео ↔ экран**
3. **Отказ от window-aware курсоров** (упрощение)
4. **Стандартизация на Fixed Aspect Ratio**

### Миграционный план

**Фаза 1: Стабилизация**

- Удалить window-aware функциональность
- Использовать только `video.videoWidth/videoHeight`
- Фиксированное соотношение сторон FFmpeg

**Фаза 2: Оптимизация**

- Добавить переключение качества (720p/1080p/1440p)
- Кэширование размеров для избежания DOM запросов

**Фаза 3: Расширение**

- Возврат к window-aware курсорам (опционально)
- Multi-monitor support

## Сравнение с крупными проектами

**TeamViewer/AnyDesk подход:**

- Фиксированные предустановленные разрешения
- Серверная сторона масштабирует под клиента
- Клиент работает в "логическом" разрешении

**Chrome Remote Desktop:**

- Адаптивное качество на основе пропускной способности
- Прямое 1:1 pixel mapping без сложных преобразований

**Ваш оптимальный подход:** Гибрид - фиксированные разрешения + адаптивное качество + простое coordinate mapping.

Такой подход решит проблему "нормализации после обновления браузера" и обеспечит стабильную работу при переносе на C++.

Хорошо, давай по фазам все будем реализовывать

# Фаза 1: Стабилизация - Упрощение координатной системы

Давайте начнем с **Фазы 1** и создадим стабильную основу с единым источником правды для координат.

## План Фазы 1

1. **Убрать window-aware функциональность**
2. **Использовать только video.videoWidth/videoHeight как источник правды**
3. **Упростить coordinate mapping до прямого 1:1**
4. **Фиксированное разрешение FFmpeg**
5. **Убрать множественные источники размеров экрана**

## Резюме изменений в Фазе 1

### ✅ Упрощено и убрано:

1. **Window-aware функциональность** - нет курсоров изменения размера окон
2. **Динамическое определение разрешения** - фиксированное 1920x1080
3. **Множественные источники размеров экрана** - только video.videoWidth/videoHeight
4. **Сложные расчеты object-fit** - прямое 1:1 масштабирование
5. **Periodic window polling** - убраны запросы информации об окнах

### ✅ Добавлено:

1. **Debug панель** с информацией о coordinate mapping
2. **Версионная информация** - показывает "Phase 1"
3. **Улучшенное логирование** - подробные сообщения о состоянии
4. **Ping/Pong механизм** - проверка живости соединения
5. **Подтверждение размеров видео** - синхронизация между браузером и агентом

Фаза 2: Оптимизация - Адаптивное качество и производительность
Переходим к Фазе 2 с добавлением переключения качества видео и оптимизации производительности.

Обновленные файлы для Фазы 2

````python
# rmm/main.py
from fastapi import FastAPI, WebSocket, WebSocketDisconnect, Request
from fastapi.middleware.cors import CORSMiddleware
from fastapi.templating import Jinja2Templates
import os
import logging
from typing import Dict
import asyncio

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
)
logger = logging.getLogger("rmm")

app = FastAPI(title="RMM Signaling Server v2.0")

app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

BASE_DIR = os.path.dirname(os.path.abspath(__file__))
templates = Jinja2Templates(directory=os.path.join(BASE_DIR, "templates"))

# WebSocket connections for WebRTC signaling only
agents: Dict[str, WebSocket] = {}
lock = asyncio.Lock()


@app.on_event("startup")
async def startup():
    asyncio.create_task(cleanup_task())


@app.get("/")
async def index(request: Request):
    """Serve the simplified viewer HTML page."""
    return templates.TemplateResponse("index.html", {"request": request})


@app.websocket("/ws/agent/{agent_id}")
async def agent_ws(ws: WebSocket, agent_id: str):
    """Handle websocket for agent connection - WebRTC signaling only."""
    await ws.accept()
    async with lock:
        agents[agent_id] = ws
    logger.info(f"Agent connected: {agent_id}")

    try:
        while True:
            try:
                data = await ws.receive_text()
            except RuntimeError as e:
                logger.error(f"Error receiving from {agent_id}: {e}")
                break
            except Exception as e:
                logger.exception(f"Unexpected error receiving from {agent_id}: {e}")
                continue

            viewer = agents.get(f"viewer:{agent_id}")
            if viewer:
                ok = await safe_send(viewer, data)
                if not ok:
                    logger.warning(f"Viewer dead, removing viewer:{agent_id}")
                    agents.pop(f"viewer:{agent_id}", None)
    except WebSocketDisconnect:
        logger.info(f"Agent disconnected: {agent_id}")
    except Exception as e:
        logger.exception(f"Error in agent socket {agent_id}: {e}")
    finally:
        async with lock:
            agents.pop(agent_id, None)


@app.websocket("/ws/viewer/{agent_id}")
async def viewer_ws(ws: WebSocket, agent_id: str):
    """Handle websocket for viewer connection - WebRTC signaling only."""
    await ws.accept()

    async with lock:
        old = agents.get(f"viewer:{agent_id}")
        if old:
            await safe_close(old)

        agents[f"viewer:{agent_id}"] = ws
        logger.info(f"Viewer connected: {agent_id}")

    try:
        while True:
            data = await ws.receive_text()
            agent = agents.get(agent_id)
            if agent:
                ok = await safe_send(agent, data)
                if not ok:
                    logger.warning(f"Agent dead, removing {agent_id}")
                    agents.pop(agent_id, None)
    except WebSocketDisconnect:
        logger.info(f"Viewer disconnected: {agent_id}")
    except Exception as e:
        logger.exception(f"Error in viewer socket {agent_id}: {e}")
    finally:
        async with lock:
            agents.pop(f"viewer:{agent_id}", None)


async def safe_close(ws: WebSocket):
    """Safely close WebSocket session."""
    try:
        await ws.close()
    except Exception as e:
        logger.warning(f"Error closing WebSocket: {e}")


async def safe_send(ws: WebSocket, data: str) -> bool:
    try:
        await ws.send_text(data)
        return True
    except Exception:
        return False


async def cleanup_task():
    while True:
        await asyncio.sleep(30)
        dead = []

        for k, ws in list(agents.items()):
            try:
                await ws.send_bytes(b"")
            except Exception:
                dead.append(k)

        for k in dead:
            logger.warning(f"Cleaning dead ws: {k}")
            agents.pop(k, None)

```HTML
<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <title>RMM Viewer v2.0 - Phase 2 Optimized</title>
    <style>
      html,
      body {
        margin: 0;
        padding: 0;
        width: 100%;
        height: 100%;
        background: #111;
        overflow: hidden;
        font-family: "Segoe UI", Tahoma, Geneva, Verdana, sans-serif;
      }

      video {
        width: 100%;
        height: 100%;
        display: block;
        object-fit: contain;
        background-color: black;
        cursor: default;
      }

      .status {
        position: fixed;
        left: 10px;
        bottom: 10px;
        color: #0f0;
        font-family: monospace;
        font-size: 12px;
        z-index: 1000;
      }

      .layout-indicator {
        position: fixed;
        right: 10px;
        bottom: 10px;
        color: #0f0;
        font-family: monospace;
        font-size: 12px;
        padding: 2px 5px;
        background: rgba(0, 0, 0, 0.5);
        border-radius: 3px;
        z-index: 1000;
      }

      .channel-status {
        position: fixed;
        left: 10px;
        top: 10px;
        color: #0f0;
        font-family: monospace;
        font-size: 10px;
        background: rgba(0, 0, 0, 0.7);
        padding: 5px;
        border-radius: 3px;
        z-index: 1000;
        min-width: 150px;
      }

      .controls {
        position: fixed;
        right: 10px;
        top: 10px;
        z-index: 1000;
        display: flex;
        flex-direction: column;
        gap: 5px;
      }

      .control-row {
        display: flex;
        gap: 5px;
        align-items: center;
      }

      .btn {
        background: rgba(0, 128, 0, 0.8);
        color: white;
        border: none;
        padding: 5px 10px;
        border-radius: 3px;
        cursor: pointer;
        font-size: 10px;
        font-family: monospace;
      }

      .btn:hover {
        background: rgba(0, 180, 0, 0.9);
      }

      .btn:disabled {
        background: rgba(128, 128, 128, 0.5);
        cursor: not-allowed;
      }

      .btn.active {
        background: rgba(0, 200, 0, 1);
        box-shadow: 0 0 5px rgba(0, 255, 0, 0.5);
      }

      .quality-selector {
        background: rgba(0, 0, 0, 0.8);
        color: #0f0;
        border: 1px solid #333;
        padding: 3px 5px;
        border-radius: 3px;
        font-size: 10px;
        font-family: monospace;
        cursor: pointer;
      }

      .quality-selector:focus {
        outline: none;
        border-color: #0f0;
      }

      .screenshot-modal {
        position: fixed;
        top: 0;
        left: 0;
        width: 100%;
        height: 100%;
        background: rgba(0, 0, 0, 0.9);
        display: none;
        z-index: 2000;
        justify-content: center;
        align-items: center;
      }

      .screenshot-content {
        max-width: 90%;
        max-height: 90%;
        border: 2px solid #0f0;
        border-radius: 5px;
      }

      .close-btn {
        position: absolute;
        top: 20px;
        right: 20px;
        background: #f00;
        color: white;
        border: none;
        padding: 10px 15px;
        border-radius: 3px;
        cursor: pointer;
        font-size: 12px;
      }

      .debug-info {
        position: fixed;
        right: 10px;
        top: 120px;
        color: #0f0;
        font-family: monospace;
        font-size: 8px;
        background: rgba(0, 0, 0, 0.8);
        padding: 8px;
        border-radius: 3px;
        z-index: 1000;
        max-width: 300px;
        border: 1px solid #333;
      }

      .version-info {
        position: fixed;
        left: 10px;
        top: 120px;
        color: #666;
        font-family: monospace;
        font-size: 8px;
        z-index: 1000;
      }

      .quality-indicator {
        position: fixed;
        right: 10px;
        bottom: 40px;
        color: #0f0;
        font-family: monospace;
        font-size: 8px;
        background: rgba(0, 0, 0, 0.7);
        padding: 3px 6px;
        border-radius: 3px;
        z-index: 1000;
        border: 1px solid #333;
      }

      .performance-stats {
        position: fixed;
        left: 10px;
        top: 160px;
        color: #0f0;
        font-family: monospace;
        font-size: 8px;
        background: rgba(0, 0, 0, 0.7);
        padding: 5px;
        border-radius: 3px;
        z-index: 1000;
        border: 1px solid #333;
        display: none;
      }
    </style>
  </head>

  <body>
    <video id="video" autoplay playsinline muted></video>

    <div id="status" class="status">Connecting...</div>
    <div id="layoutIndicator" class="layout-indicator">EN</div>

    <div class="channel-status">
      <div>Control: <span id="controlStatus">Disconnected</span></div>
      <div>Binary: <span id="binaryStatus">Disconnected</span></div>
      <div>Video: <span id="videoStatus">Waiting</span></div>
      <div>Quality: <span id="currentQuality">-</span></div>
    </div>

    <div class="version-info">
      RMM v2.0 - Phase 2 Optimized<br />
      Adaptive Quality + Performance
    </div>

    <div class="quality-indicator">
      <div>Res: <span id="qualityRes">-</span></div>
      <div>FPS: <span id="qualityFps">-</span></div>
      <div>Ping: <span id="qualityPing">-</span>ms</div>
    </div>

    <div class="performance-stats" id="performanceStats">
      <div><strong>PERFORMANCE</strong></div>
      <hr style="margin: 2px 0; border-color: #333" />
      <div>Coord Cache: <span id="perfCoordCache">0</span> hits</div>
      <div>Mouse Events: <span id="perfMouseEvents">0</span>/s</div>
      <div>Frame Rate: <span id="perfFrameRate">0</span> fps</div>
      <div>Bandwidth: <span id="perfBandwidth">0</span> Mbps</div>
      <div>Latency: <span id="perfLatency">0</span>ms</div>
    </div>

    <div class="controls">
      <div class="control-row">
        <select id="qualitySelector" class="quality-selector">
          <option value="720p">720p (Fast)</option>
          <option value="1080p" selected>1080p (Balanced)</option>
          <option value="1440p">1440p (Quality)</option>
          <option value="auto">Auto Adapt</option>
        </select>
        <button id="applyQuality" class="btn">Apply</button>
      </div>

      <div class="control-row">
        <button id="screenshotBtn" class="btn" disabled>Screenshot (F9)</button>
        <button id="debugBtn" class="btn">Debug</button>
        <button id="perfBtn" class="btn">Perf</button>
      </div>
    </div>

    <!-- Debug Info -->
    <div id="debugInfo" class="debug-info" style="display: none">
      <div><strong>PHASE 2 DEBUG - OPTIMIZED</strong></div>
      <hr style="margin: 3px 0; border-color: #333" />
      <div>Video Native: <span id="debugVideoSize">-</span></div>
      <div>Element Size: <span id="debugElementSize">-</span></div>
      <div>Draw Area: <span id="debugDrawArea">-</span></div>
      <div>Cached Mapping: <span id="debugCachedMapping">-</span></div>
      <div>Mouse Local: <span id="debugMouseCoords">-</span></div>
      <div>Mouse Remote: <span id="debugRemoteCoords">-</span></div>
      <div>Scale Factor: <span id="debugScaleFactor">-</span></div>
      <div>Cache Hits: <span id="debugCacheHits">0</span></div>
      <div>Last Update: <span id="debugLastUpdate">-</span></div>
    </div>

    <!-- Screenshot Modal -->
    <div id="screenshotModal" class="screenshot-modal">
      <button class="close-btn" onclick="closeScreenshot()">Close (ESC)</button>
      <img id="screenshotImg" class="screenshot-content" />
    </div>

    <script>
      // === RMM VIEWER v2.0 - PHASE 2: OPTIMIZED WITH ADAPTIVE QUALITY ===
      console.log(
        "🚀 RMM Viewer v2.0 - Phase 2: Optimized with Adaptive Quality",
      );

      // WebRTC и WebSocket variables
      let pc, ws;
      let controlChannel, binaryChannel;

      // PHASE 2: Quality Management
      const QUALITY_PRESETS = {
        "720p": {
          width: 1280,
          height: 720,
          bitrate: "2M",
          maxrate: "3M",
          bufsize: "4M",
        },
        "1080p": {
          width: 1920,
          height: 1080,
          bitrate: "4M",
          maxrate: "6M",
          bufsize: "8M",
        },
        "1440p": {
          width: 2560,
          height: 1440,
          bitrate: "8M",
          maxrate: "12M",
          bufsize: "16M",
        },
      };

      let currentQuality = "1080p";
      let autoQualityEnabled = false;

      // PHASE 2: Coordinate mapping cache
      let coordinateCache = {
        videoNativeWidth: 0,
        videoNativeHeight: 0,
        elementRect: null,
        drawArea: null,
        lastCacheUpdate: 0,
        cacheHits: 0,
        cacheDuration: 100, // ms
      };

      let lastMouseMove = 0;
      const mouseMoveThrottle = 8; // OPTIMIZED: Higher frequency for better responsiveness
      let currentLayout = "en";
      let screenshotCounter = 0;

      // PHASE 2: Performance monitoring
      let performanceStats = {
        mouseEventsPerSecond: 0,
        mouseEventCount: 0,
        frameRate: 0,
        bandwidth: 0,
        latency: 0,
        lastStatsUpdate: 0,
      };

      // DOM elements
      const statusEl = document.getElementById("status");
      const controlStatusEl = document.getElementById("controlStatus");
      const binaryStatusEl = document.getElementById("binaryStatus");
      const videoStatusEl = document.getElementById("videoStatus");
      const currentQualityEl = document.getElementById("currentQuality");
      const layoutIndicator = document.getElementById("layoutIndicator");
      const video = document.getElementById("video");
      const screenshotBtn = document.getElementById("screenshotBtn");
      const screenshotModal = document.getElementById("screenshotModal");
      const screenshotImg = document.getElementById("screenshotImg");
      const qualitySelector = document.getElementById("qualitySelector");
      const applyQualityBtn = document.getElementById("applyQuality");

      // Quality indicator elements
      const qualityRes = document.getElementById("qualityRes");
      const qualityFps = document.getElementById("qualityFps");
      const qualityPing = document.getElementById("qualityPing");

      // Debug elements
      const debugInfo = document.getElementById("debugInfo");
      const debugVideoSize = document.getElementById("debugVideoSize");
      const debugElementSize = document.getElementById("debugElementSize");
      const debugDrawArea = document.getElementById("debugDrawArea");
      const debugCachedMapping = document.getElementById("debugCachedMapping");
      const debugMouseCoords = document.getElementById("debugMouseCoords");
      const debugRemoteCoords = document.getElementById("debugRemoteCoords");
      const debugScaleFactor = document.getElementById("debugScaleFactor");
      const debugCacheHits = document.getElementById("debugCacheHits");
      const debugLastUpdate = document.getElementById("debugLastUpdate");

      // Performance elements
      const performanceStatsEl = document.getElementById("performanceStats");
      const perfCoordCache = document.getElementById("perfCoordCache");
      const perfMouseEvents = document.getElementById("perfMouseEvents");
      const perfFrameRate = document.getElementById("perfFrameRate");
      const perfBandwidth = document.getElementById("perfBandwidth");
      const perfLatency = document.getElementById("perfLatency");

      // Cyrillic to Latin mapping
      const cyrillicToLatinMap = {
        й: "q",
        ц: "w",
        у: "e",
        к: "r",
        е: "t",
        н: "y",
        г: "u",
        ш: "i",
        щ: "o",
        з: "p",
        х: "[",
        ъ: "]",
        ф: "a",
        ы: "s",
        в: "d",
        а: "f",
        п: "g",
        р: "h",
        о: "j",
        л: "k",
        д: "l",
        ж: ";",
        э: "'",
        я: "z",
        ч: "x",
        с: "c",
        м: "v",
        и: "b",
        т: "n",
        ь: "m",
        б: ",",
        ю: ".",
        "/": "/",
        ё: "`",
        Ё: "~",
      };

      // Prevent context menu
      document.addEventListener("contextmenu", (e) => e.preventDefault());

      // === HELPER FUNCTIONS ===
      function setStatus(msg, color = "#0f0") {
        statusEl.textContent = msg;
        statusEl.style.color = color;
        console.log(`[STATUS] ${msg}`);
      }

      function setChannelStatus(channel, status, color = "#0f0") {
        const el =
          channel === "control"
            ? controlStatusEl
            : channel === "binary"
              ? binaryStatusEl
              : videoStatusEl;
        el.textContent = status;
        el.style.color = color;
      }

      function updateLayoutIndicator() {
        layoutIndicator.textContent = currentLayout === "ru" ? "RU" : "EN";
      }

      function updateQualityIndicator(quality, fps = "-", ping = "-") {
        currentQualityEl.textContent = quality;
        qualityRes.textContent = quality;
        qualityFps.textContent = fps;
        qualityPing.textContent = ping;
      }

      // PHASE 2: Optimized coordinate cache
      function updateCoordinateCache() {
        const now = Date.now();
        if (
          now - coordinateCache.lastCacheUpdate <
          coordinateCache.cacheDuration
        ) {
          coordinateCache.cacheHits++;
          return false; // Cache is still valid
        }

        coordinateCache.videoNativeWidth = video.videoWidth;
        coordinateCache.videoNativeHeight = video.videoHeight;
        coordinateCache.elementRect = video.getBoundingClientRect();

        if (
          coordinateCache.videoNativeWidth &&
          coordinateCache.videoNativeHeight &&
          coordinateCache.elementRect
        ) {
          const videoNativeRatio =
            coordinateCache.videoNativeWidth /
            coordinateCache.videoNativeHeight;
          const elementRatio =
            coordinateCache.elementRect.width /
            coordinateCache.elementRect.height;

          let drawWidth,
            drawHeight,
            offsetX = 0,
            offsetY = 0;

          if (elementRatio > videoNativeRatio) {
            drawHeight = coordinateCache.elementRect.height;
            drawWidth = drawHeight * videoNativeRatio;
            offsetX = (coordinateCache.elementRect.width - drawWidth) / 2;
          } else {
            drawWidth = coordinateCache.elementRect.width;
            drawHeight = drawWidth / videoNativeRatio;
            offsetY = (coordinateCache.elementRect.height - drawHeight) / 2;
          }

          coordinateCache.drawArea = {
            drawWidth,
            drawHeight,
            offsetX,
            offsetY,
          };
          coordinateCache.lastCacheUpdate = now;
          return true; // Cache updated
        }

        return false;
      }

      function updateDebugInfo(coords = {}) {
        if (debugInfo.style.display === "block") {
          debugVideoSize.textContent = `${coordinateCache.videoNativeWidth}x${coordinateCache.videoNativeHeight}`;

          if (coordinateCache.elementRect) {
            debugElementSize.textContent = `${Math.round(coordinateCache.elementRect.width)}x${Math.round(coordinateCache.elementRect.height)}`;
          }

          if (coordinateCache.drawArea) {
            debugDrawArea.textContent = `${Math.round(coordinateCache.drawArea.drawWidth)}x${Math.round(coordinateCache.drawArea.drawHeight)}`;
            debugCachedMapping.textContent = `${Math.round(coordinateCache.drawArea.offsetX)},${Math.round(coordinateCache.drawArea.offsetY)}`;
          }

          if (coords.mouseX !== undefined && coords.mouseY !== undefined) {
            debugMouseCoords.textContent = `${Math.round(coords.mouseX)},${Math.round(coords.mouseY)}`;
          }

          if (coords.remoteX !== undefined && coords.remoteY !== undefined) {
            debugRemoteCoords.textContent = `${coords.remoteX},${coords.remoteY}`;
          }

          if (coordinateCache.videoNativeWidth && coordinateCache.drawArea) {
            const scaleX =
              coordinateCache.videoNativeWidth /
              coordinateCache.drawArea.drawWidth;
            const scaleY =
              coordinateCache.videoNativeHeight /
              coordinateCache.drawArea.drawHeight;
            debugScaleFactor.textContent = `${scaleX.toFixed(3)}x${scaleY.toFixed(3)}`;
          }

          debugCacheHits.textContent = coordinateCache.cacheHits;
          debugLastUpdate.textContent = new Date().toLocaleTimeString();
        }
      }

      // PHASE 2: Optimized coordinate mapping with caching
      function mapPointerToRemote(clientX, clientY) {
        updateCoordinateCache();

        if (
          !coordinateCache.videoNativeWidth ||
          !coordinateCache.videoNativeHeight ||
          !coordinateCache.drawArea
        ) {
          console.warn("[COORDS] Cache not ready");
          return null;
        }

        const mouseX = clientX - coordinateCache.elementRect.left;
        const mouseY = clientY - coordinateCache.elementRect.top;

        if (
          mouseX < 0 ||
          mouseX > coordinateCache.elementRect.width ||
          mouseY < 0 ||
          mouseY > coordinateCache.elementRect.height
        ) {
          return null;
        }

        const xInVideo = mouseX - coordinateCache.drawArea.offsetX;
        const yInVideo = mouseY - coordinateCache.drawArea.offsetY;

        if (
          xInVideo < 0 ||
          xInVideo > coordinateCache.drawArea.drawWidth ||
          yInVideo < 0 ||
          yInVideo > coordinateCache.drawArea.drawHeight
        ) {
          return null;
        }

        const remoteX = Math.round(
          (xInVideo / coordinateCache.drawArea.drawWidth) *
            coordinateCache.videoNativeWidth,
        );
        const remoteY = Math.round(
          (yInVideo / coordinateCache.drawArea.drawHeight) *
            coordinateCache.videoNativeHeight,
        );

        const clampedX = Math.max(
          0,
          Math.min(coordinateCache.videoNativeWidth - 1, remoteX),
        );
        const clampedY = Math.max(
          0,
          Math.min(coordinateCache.videoNativeHeight - 1, remoteY),
        );

        updateDebugInfo({
          mouseX,
          mouseY,
          remoteX: clampedX,
          remoteY: clampedY,
        });

        return { x: clampedX, y: clampedY };
      }

      // === COMMUNICATION FUNCTIONS ===
      function sendControl(type, payload = {}) {
        if (controlChannel && controlChannel.readyState === "open") {
          try {
            const message = { type, timestamp: Date.now(), ...payload };
            controlChannel.send(JSON.stringify(message));
            console.log(`[CONTROL] Sent: ${type}`, payload);
          } catch (e) {
            console.error("[CONTROL] Failed to send message:", e);
          }
        } else {
          console.warn(`[CONTROL] Channel not ready for ${type}`);
        }
      }

      function sendBinary(messageType, payload = new Uint8Array()) {
        if (binaryChannel && binaryChannel.readyState === "open") {
          try {
            const typeBytes = new TextEncoder().encode(
              messageType.padEnd(4).slice(0, 4),
            );
            const message = new Uint8Array(typeBytes.length + payload.length);
            message.set(typeBytes, 0);
            message.set(payload, typeBytes.length);
            binaryChannel.send(message);
            console.log(
              `[BINARY] Sent: ${messageType}, ${payload.length} bytes`,
            );
          } catch (e) {
            console.error("[BINARY] Failed to send message:", e);
          }
        } else {
          console.warn(`[BINARY] Channel not ready for ${messageType}`);
        }
      }

      // PHASE 2: Quality management
      function changeQuality(quality) {
        if (quality === "auto") {
          autoQualityEnabled = true;
          currentQuality = "1080p"; // Default for auto
          console.log("[QUALITY] Auto-adapt mode enabled");
        } else {
          autoQualityEnabled = false;
          currentQuality = quality;
          console.log(`[QUALITY] Manual quality set to: ${quality}`);
        }

        const preset = QUALITY_PRESETS[currentQuality];
        if (preset) {
          sendControl("change_quality", {
            quality: currentQuality,
            width: preset.width,
            height: preset.height,
            bitrate: preset.bitrate,
            maxrate: preset.maxrate,
            bufsize: preset.bufsize,
            autoAdapt: autoQualityEnabled,
          });

          updateQualityIndicator(currentQuality);

          // Reset coordinate cache when quality changes
          coordinateCache.lastCacheUpdate = 0;
          coordinateCache.cacheHits = 0;
        }
      }

      // === VIDEO DIMENSION SYNCHRONIZATION ===
      function syncVideoDimensions() {
        if (video.videoWidth && video.videoHeight) {
          const newWidth = video.videoWidth;
          const newHeight = video.videoHeight;

          if (
            coordinateCache.videoNativeWidth !== newWidth ||
            coordinateCache.videoNativeHeight !== newHeight
          ) {
            const prevWidth = coordinateCache.videoNativeWidth;
            const prevHeight = coordinateCache.videoNativeHeight;

            // Force cache update
            coordinateCache.lastCacheUpdate = 0;
            updateCoordinateCache();

            console.log(
              `[VIDEO] Native dimensions updated: ${prevWidth}x${prevHeight} → ${newWidth}x${newHeight}`,
            );
            updateDebugInfo();

            sendControl("video_dimensions_confirmed", {
              width: newWidth,
              height: newHeight,
            });
          }
        }
      }

      // Video dimension monitoring
      video.addEventListener("loadedmetadata", () => {
        console.log("[VIDEO] Metadata loaded");
        syncVideoDimensions();
      });

      video.addEventListener("resize", () => {
        console.log("[VIDEO] Video resized");
        syncVideoDimensions();
      });

      // Monitor video element size changes
      new ResizeObserver(() => {
        coordinateCache.lastCacheUpdate = 0; // Force cache refresh on resize
        updateDebugInfo();
      }).observe(video);

      // === MOUSE EVENT HANDLERS ===
      video.addEventListener("mousemove", (e) => {
        const now = Date.now();
        if (now - lastMouseMove < mouseMoveThrottle) return;
        lastMouseMove = now;

        // Performance tracking
        performanceStats.mouseEventCount++;

        const mapped = mapPointerToRemote(e.clientX, e.clientY);
        if (mapped) {
          sendControl("mouse_move", mapped);
        }
      });

      // Mouse click events
      ["mousedown", "mouseup"].forEach((evt) =>
        video.addEventListener(evt, (e) => {
          const mapped = mapPointerToRemote(e.clientX, e.clientY);
          if (mapped) {
            sendControl(evt === "mousedown" ? "mouse_down" : "mouse_up", {
              button: e.button,
              x: mapped.x,
              y: mapped.y,
            });
          }
          e.preventDefault();
        }),
      );

      // === KEYBOARD EVENT HANDLERS ===
      function isPrintableKey(e) {
        return (
          e.key.length === 1 &&
          !e.ctrlKey &&
          !e.altKey &&
          !e.metaKey &&
          e.key !== " "
        );
      }

      function mapKeyEventToRobotKey(e) {
        const tagName = e.target.tagName;
        if (
          tagName === "INPUT" ||
          tagName === "TEXTAREA" ||
          e.target.isContentEditable
        ) {
          return null;
        }

        if (e.code === "Space") return "space";

        const keyMap = {
          Enter: "enter",
          Backspace: "backspace",
          Tab: "tab",
          Escape: "esc",
          Delete: "delete",
          Insert: "insert",
          Home: "home",
          End: "end",
          PageUp: "pageup",
          PageDown: "pagedown",
          ArrowLeft: "left",
          ArrowRight: "right",
          ArrowUp: "up",
          ArrowDown: "down",
          ControlLeft: "ctrl",
          ControlRight: "ctrl",
          ShiftLeft: "shift",
          ShiftRight: "shift",
          AltLeft: "alt",
          AltRight: "alt",
          F1: "f1",
          F2: "f2",
          F3: "f3",
          F4: "f4",
          F5: "f5",
          F6: "f6",
          F7: "f7",
          F8: "f8",
          F9: "f9",
          F10: "f10",
          F11: "f11",
          F12: "f12",
          BracketLeft: "[",
          BracketRight: "]",
          Semicolon: ";",
          Quote: "'",
          Comma: ",",
          Period: ".",
          Slash: "/",
          Backslash: "\\",
          Equal: "=",
          Minus: "-",
          Backquote: "`",
        };

        return keyMap[e.code] || null;
      }

      window.addEventListener("keydown", (e) => {
        if (!controlChannel || controlChannel.readyState !== "open") return;

        // F9 for screenshot
        if (e.key === "F9") {
          e.preventDefault();
          requestScreenshot();
          return;
        }

        // ESC to close screenshot
        if (e.key === "Escape" && screenshotModal.style.display === "flex") {
          closeScreenshot();
          e.preventDefault();
          return;
        }

        // Handle printable keys
        if (isPrintableKey(e) && e.key.length === 1) {
          let latKey = e.key;
          if (/[а-яёА-ЯЁ]/.test(latKey)) {
            latKey = cyrillicToLatinMap[latKey.toLowerCase()] || latKey;
          }
          sendControl("key_down", { key: latKey });
          e.preventDefault();
          return;
        }

        // Handle special keys
        const robotKey = mapKeyEventToRobotKey(e);
        if (robotKey) {
          e.preventDefault();
          sendControl("key_down", { key: robotKey });
        } else {
          e.preventDefault();
        }
      });

      window.addEventListener("keyup", (e) => {
        if (!controlChannel || controlChannel.readyState !== "open") return;

        if (
          isPrintableKey(e) &&
          e.key.length === 1 &&
          /[а-яёА-ЯЁ]/.test(e.key)
        ) {
          const latKey = cyrillicToLatinMap[e.key.toLowerCase()] || e.key;
          sendControl("key_up", { key: latKey });
          e.preventDefault();
          return;
        }

        const robotKey = mapKeyEventToRobotKey(e);
        if (robotKey) {
          e.preventDefault();
          sendControl("key_up", { key: robotKey });
        } else {
          e.preventDefault();
        }
      });

      // Layout switching (Ctrl+Shift)
      window.addEventListener("keydown", (e) => {
        if (e.ctrlKey && e.shiftKey && !e.altKey && !e.metaKey) {
          currentLayout = currentLayout === "ru" ? "en" : "ru";
          updateLayoutIndicator();
          console.log(`[KEYBOARD] Layout switched to: ${currentLayout}`);
          e.preventDefault();
        }
      });

      // === WebRTC CONNECTION SETUP ===
      async function connect() {
        const addr =
          "ws://" + window.location.hostname + ":8000/ws/viewer/agent1";
        console.log(`[WS] Connecting to: ${addr}`);

        ws = new WebSocket(addr);

        ws.onopen = async () => {
          console.log("[WS] Connected successfully");
          setStatus("Connected");

          if (pc) {
            pc.close();
          }

          pc = new RTCPeerConnection({
            iceServers: [{ urls: "stun:stun.l.google.com:19302" }],
          });

          controlChannel = pc.createDataChannel("control", { ordered: true });
          binaryChannel = pc.createDataChannel("binary", { ordered: true });

          setupControlChannel(controlChannel);
          setupBinaryChannel(binaryChannel);
          setupPeerConnection(pc);

          try {
            const offer = await pc.createOffer();
            await pc.setLocalDescription(offer);
            ws.send(JSON.stringify(offer));
            console.log("[WebRTC] SDP offer sent");
          } catch (err) {
            console.error("[WebRTC] Error creating offer:", err);
            setStatus("WebRTC Error", "#f00");
            pc.close();
            ws.close();
          }
        };

        ws.onmessage = async (e) => {
          try {
            if (typeof e.data === "string") {
              const data = JSON.parse(e.data);
              if (data.type === "answer") {
                await pc.setRemoteDescription(data);
                console.log("[WebRTC] SDP answer received");
              } else if (data.candidate) {
                await pc.addIceCandidate(data);
                console.log("[WebRTC] ICE candidate received");
              } else {
                console.log("[WS] Unknown message type:", data.type);
              }
            }
          } catch (err) {
            console.error("[WS] Error handling message:", err);
          }
        };

        ws.onclose = () => {
          console.warn("[WS] Disconnected, retrying in 2 seconds...");
          setStatus("Reconnecting...", "#f90");
          setChannelStatus("control", "Disconnected", "#f90");
          setChannelStatus("binary", "Disconnected", "#f90");
          setChannelStatus("video", "Disconnected", "#f90");
          if (pc) pc.close();
          setTimeout(connect, 2000);
        };

        ws.onerror = (err) => {
          console.error("[WS] Error:", err);
          setStatus("WS Error", "#f00");
        };
      }

      // === DATA CHANNEL SETUP ===
      function setupControlChannel(channel) {
        channel.onopen = () => {
          console.log("[CONTROL] DataChannel opened");
          setStatus("Control Ready");
          setChannelStatus("control", "Connected", "#0f0");
          screenshotBtn.disabled = false;

          // Send initial quality settings
          sendControl("change_quality", {
            quality: currentQuality,
            ...QUALITY_PRESETS[currentQuality],
            autoAdapt: autoQualityEnabled,
          });
        };

        channel.onmessage = (e) => {
          try {
            const data = JSON.parse(e.data);
            handleControlMessage(data);
          } catch (err) {
            console.warn("[CONTROL] Failed to parse message:", err, e.data);
          }
        };

        channel.onclose = () => {
          console.log("[CONTROL] DataChannel closed");
          setChannelStatus("control", "Disconnected", "#f66");
          screenshotBtn.disabled = true;
        };

        channel.onerror = (err) => {
          console.error("[CONTROL] DataChannel error:", err);
        };
      }

      function setupBinaryChannel(channel) {
        channel.binaryType = "arraybuffer";

        channel.onopen = () => {
          console.log("[BINARY] DataChannel opened");
          setChannelStatus("binary", "Connected", "#0f0");
        };

        channel.onmessage = (e) => {
          handleBinaryMessage(e.data);
        };

        channel.onclose = () => {
          console.log("[BINARY] DataChannel closed");
          setChannelStatus("binary", "Disconnected", "#f66");
        };

        channel.onerror = (err) => {
          console.error("[BINARY] DataChannel error:", err);
        };
      }

      function setupPeerConnection(pcInstance) {
        pcInstance.ontrack = (e) => {
          if (video.srcObject !== e.streams[0]) {
            video.srcObject = e.streams[0];
            setChannelStatus("video", "Streaming", "#0f0");
            console.log("[WebRTC] Video stream received");

            setTimeout(syncVideoDimensions, 1000);
          }
        };

        pcInstance.onicecandidate = (e) => {
          if (e.candidate) {
            ws.send(JSON.stringify(e.candidate));
          }
        };

        pcInstance.onconnectionstatechange = () => {
          const state = pcInstance.connectionState;
          console.log(`[WebRTC] Connection state: ${state}`);

          if (state === "connected") {
            setStatus("Full Connected", "#0f0");
            startPerformanceMonitoring();
          } else if (state === "failed" || state === "closed") {
            setStatus("Connection Lost", "#f00");
            setChannelStatus("video", "Disconnected", "#f00");
            if (ws && ws.readyState === WebSocket.OPEN) {
              ws.close();
            }
          } else if (state === "connecting") {
            setStatus("Connecting...", "#ff0");
          }
        };

        pcInstance.addTransceiver("video", { direction: "recvonly" });
      }

      // === MESSAGE HANDLING ===
      function handleControlMessage(data) {
        console.log("[CONTROL] Received:", data);

        // Calculate latency if timestamp provided
        if (data.timestamp) {
          performanceStats.latency = Date.now() - data.timestamp;
          qualityPing.textContent = performanceStats.latency;
        }

        switch (data.type) {
          case "video_info":
            if (data.width && data.height) {
              console.log(
                `[CONTROL] Server video info: ${data.width}x${data.height}`,
              );

              if (
                coordinateCache.videoNativeWidth &&
                coordinateCache.videoNativeHeight
              ) {
                if (
                  coordinateCache.videoNativeWidth !== data.width ||
                  coordinateCache.videoNativeHeight !== data.height
                ) {
                  console.warn(
                    `[CONTROL] Video size mismatch! Local: ${coordinateCache.videoNativeWidth}x${coordinateCache.videoNativeHeight}, Server: ${data.width}x${data.height}`,
                  );
                } else {
                  console.log("[CONTROL] Video dimensions confirmed matching");
                }
              }
            }
            break;

          case "quality_changed":
            if (data.quality) {
              currentQuality = data.quality;
              updateQualityIndicator(data.quality, data.fps);
              console.log(
                `[QUALITY] Server confirmed quality change: ${data.quality}`,
              );
            }
            break;

          case "performance_stats":
            if (data.fps) performanceStats.frameRate = data.fps;
            if (data.bandwidth) performanceStats.bandwidth = data.bandwidth;
            updatePerformanceDisplay();
            break;

          case "pong":
            console.log("[CONTROL] Pong received");
            break;

          default:
            console.log("[CONTROL] Unknown message type:", data.type);
        }
      }

      function handleBinaryMessage(arrayBuffer) {
        const data = new Uint8Array(arrayBuffer);
        if (data.length < 4) {
          console.warn("[BINARY] Message too short:", data.length);
          return;
        }

        const msgType = String.fromCharCode(...data.slice(0, 4));
        const payload = data.slice(4);

        console.log(
          `[BINARY] Received: ${msgType.trim()}, ${payload.length} bytes`,
        );

        switch (msgType.trim()) {
          case "SCRN":
            handleScreenshotData(payload);
            break;
          default:
            console.log("[BINARY] Unknown message type:", msgType);
        }
      }

      function handleScreenshotData(data) {
        screenshotCounter++;
        console.log(
          `[SCREENSHOT] #${screenshotCounter} received: ${data.length} bytes`,
        );

        const blob = new Blob([data], { type: "image/png" });
        const url = URL.createObjectURL(blob);

        screenshotImg.src = url;
        screenshotModal.style.display = "flex";

        setTimeout(() => URL.revokeObjectURL(url), 30000);
      }

      // === PERFORMANCE MONITORING ===
      function startPerformanceMonitoring() {
        setInterval(() => {
          const now = Date.now();
          const timeDiff = (now - performanceStats.lastStatsUpdate) / 1000;

          if (timeDiff >= 1) {
            performanceStats.mouseEventsPerSecond = Math.round(
              performanceStats.mouseEventCount / timeDiff,
            );
            performanceStats.mouseEventCount = 0;
            performanceStats.lastStatsUpdate = now;

            updatePerformanceDisplay();
          }
        }, 1000);
      }

      function updatePerformanceDisplay() {
        if (performanceStatsEl.style.display !== "none") {
          perfCoordCache.textContent = coordinateCache.cacheHits;
          perfMouseEvents.textContent = performanceStats.mouseEventsPerSecond;
          perfFrameRate.textContent = performanceStats.frameRate;
          perfBandwidth.textContent = (
            performanceStats.bandwidth / 1000000
          ).toFixed(1); // Convert to Mbps
          perfLatency.textContent = performanceStats.latency;
        }
      }

      // === UI FUNCTIONS ===
      function requestScreenshot() {
        if (!binaryChannel || binaryChannel.readyState !== "open") {
          console.warn("[SCREENSHOT] Binary channel not available");
          return;
        }

        console.log("[SCREENSHOT] Requesting...");
        sendBinary("SCRN", new TextEncoder().encode("REQUEST"));
      }

      function closeScreenshot() {
        screenshotModal.style.display = "none";
        if (screenshotImg.src && screenshotImg.src.startsWith("blob:")) {
          URL.revokeObjectURL(screenshotImg.src);
          screenshotImg.src = "";
        }
      }

      function toggleDebug() {
        const isVisible = debugInfo.style.display !== "none";
        debugInfo.style.display = isVisible ? "none" : "block";
        console.log(`[DEBUG] Panel ${isVisible ? "hidden" : "shown"}`);

        if (!isVisible) {
          updateDebugInfo();
        }
      }

      function togglePerformance() {
        const isVisible = performanceStatsEl.style.display !== "none";
        performanceStatsEl.style.display = isVisible ? "none" : "block";
        console.log(`[PERFORMANCE] Panel ${isVisible ? "hidden" : "shown"}`);

        if (!isVisible) {
          updatePerformanceDisplay();
        }
      }

      // === EVENT LISTENERS ===
      screenshotBtn.addEventListener("click", requestScreenshot);
      document
        .getElementById("debugBtn")
        .addEventListener("click", toggleDebug);
      document
        .getElementById("perfBtn")
        .addEventListener("click", togglePerformance);

      applyQualityBtn.addEventListener("click", () => {
        const selectedQuality = qualitySelector.value;
        changeQuality(selectedQuality);
        applyQualityBtn.classList.add("active");
        setTimeout(() => applyQualityBtn.classList.remove("active"), 2000);
      });

      screenshotModal.addEventListener("click", (e) => {
        if (e.target === screenshotModal) {
          closeScreenshot();
        }
      });

      // === INITIALIZATION ===
      console.log("[INIT] Starting RMM Viewer v2.0 - Phase 2 Optimized");
      updateLayoutIndicator();
      updateQualityIndicator(currentQuality);
      connect();

      // Periodic ping with latency measurement
      setInterval(() => {
        if (controlChannel && controlChannel.readyState === "open") {
          sendControl("ping");
        }
      }, 10000); // Every 10 seconds

      // Auto quality adaptation based on performance
      setInterval(() => {
        if (autoQualityEnabled && performanceStats.latency > 0) {
          if (performanceStats.latency > 200 && currentQuality === "1440p") {
            changeQuality("1080p");
            console.log(
              "[AUTO-QUALITY] Downgraded to 1080p due to high latency",
            );
          } else if (
            performanceStats.latency > 500 &&
            currentQuality === "1080p"
          ) {
            changeQuality("720p");
            console.log(
              "[AUTO-QUALITY] Downgraded to 720p due to high latency",
            );
          } else if (
            performanceStats.latency < 100 &&
            currentQuality === "720p"
          ) {
            changeQuality("1080p");
            console.log("[AUTO-QUALITY] Upgraded to 1080p due to good latency");
          } else if (
            performanceStats.latency < 50 &&
            currentQuality === "1080p"
          ) {
            changeQuality("1440p");
            console.log(
              "[AUTO-QUALITY] Upgraded to 1440p due to excellent latency",
            );
          }
        }
      }, 30000); // Check every 30 seconds
    </script>
  </body>
</html>

````

```GO
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-vgo/robotgo"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

const (
	serverURL           = "ws://192.168.2.222:8000/ws/agent/agent1"
	websocketMaxRetries = 5
	websocketRetryDelay = 5 * time.Second

	// DEFAULT resolution - can be changed via quality commands
	DEFAULT_WIDTH     = 1920
	DEFAULT_HEIGHT    = 1080
	DEFAULT_FRAMERATE = 30
)

func init() {
	if runtime.GOOS == "windows" {
		initWindowsDPI()
	}
}

// === QUALITY CONFIGURATION ===
type QualityConfig struct {
	Width   int
	Height  int
	Bitrate string
	Maxrate string
	Bufsize string
	FPS     int
}

var QUALITY_PRESETS = map[string]QualityConfig{
	"720p": {
		Width: 1280, Height: 720,
		Bitrate: "2M", Maxrate: "3M", Bufsize: "4M",
		FPS: 30,
	},
	"1080p": {
		Width: 1920, Height: 1080,
		Bitrate: "4M", Maxrate: "6M", Bufsize: "8M",
		FPS: 30,
	},
	"1440p": {
		Width: 2560, Height: 1440,
		Bitrate: "8M", Maxrate: "12M", Bufsize: "16M",
		FPS: 30,
	},
}

// === GLOBAL VARIABLES ===
var (
	user32      = syscall.NewLazyDLL("user32.dll")
	setDPIAware = user32.NewProc("SetProcessDPIAware")

	videoBytesSent  int64
	videoFramesSent int64
	videoStatsLock  sync.Mutex

	ffmpegRestartSignal = make(chan struct{}, 1)
	ffmpegMutex         sync.Mutex
	ffmpegStatsReset    = make(chan struct{}, 1)
	currentFFmpegCmd    *exec.Cmd

	// PHASE 2: Dynamic quality settings
	currentQuality = "1080p"
	qualityMutex   sync.RWMutex
)

// === CHANNEL MANAGER ===
type ChannelManager struct {
	controlChannel *webrtc.DataChannel
	binaryChannel  *webrtc.DataChannel
	mutex          sync.RWMutex
}

var channelManager = &ChannelManager{}

func initWindowsDPI() {
	setDPIAware.Call()
}

// === QUALITY MANAGEMENT ===
func getCurrentQualityConfig() QualityConfig {
	qualityMutex.RLock()
	defer qualityMutex.RUnlock()

	if config, exists := QUALITY_PRESETS[currentQuality]; exists {
		return config
	}
	return QUALITY_PRESETS["1080p"] // fallback
}

func setCurrentQuality(quality string) {
	qualityMutex.Lock()
	defer qualityMutex.Unlock()

	if _, exists := QUALITY_PRESETS[quality]; exists {
		currentQuality = quality
		log.Printf("[QUALITY] Changed to: %s", quality)
	} else {
		log.Printf("[QUALITY] Unknown quality: %s, keeping: %s", quality, currentQuality)
	}
}

// === COMMUNICATION FUNCTIONS ===
func sendControlMessage(data map[string]interface{}) error {
	channelManager.mutex.RLock()
	dc := channelManager.controlChannel
	channelManager.mutex.RUnlock()

	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return fmt.Errorf("control channel not available")
	}

	// Add timestamp for latency measurement
	data["timestamp"] = time.Now().UnixMilli()

	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("json marshal error: %v", err)
	}

	return dc.SendText(string(jsonData))
}

func sendBinaryData(messageType string, payload []byte) error {
	channelManager.mutex.RLock()
	dc := channelManager.binaryChannel
	channelManager.mutex.RUnlock()

	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return fmt.Errorf("binary channel not available")
	}

	typeBytes := []byte(messageType)
	if len(typeBytes) < 4 {
		typeBytes = append(typeBytes, make([]byte, 4-len(typeBytes))...)
	} else if len(typeBytes) > 4 {
		typeBytes = typeBytes[:4]
	}

	message := append(typeBytes, payload...)
	return dc.Send(message)
}

// === DYNAMIC VIDEO INFO ===
func sendVideoInfo() {
	config := getCurrentQualityConfig()

	info := map[string]interface{}{
		"type":    "video_info",
		"width":   config.Width,
		"height":  config.Height,
		"quality": currentQuality,
		"fps":     config.FPS,
	}

	if err := sendControlMessage(info); err != nil {
		log.Printf("[ERROR] Failed to send video_info: %v", err)
	} else {
		log.Printf("[VIDEO] Sent video_info: %dx%d (%s)", config.Width, config.Height, currentQuality)
	}
}

// === SCREENSHOT FUNCTIONS ===
func captureScreenshot() ([]byte, error) {
	log.Println("[SCREENSHOT] Capturing screen...")

	bitmap := robotgo.CaptureScreen()
	if bitmap == nil {
		return nil, fmt.Errorf("failed to capture screen")
	}
	defer robotgo.FreeBitmap(bitmap)

	img := robotgo.ToImage(bitmap)
	if img == nil {
		return nil, fmt.Errorf("failed to convert bitmap to image")
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("failed to encode PNG: %v", err)
	}

	log.Printf("[SCREENSHOT] Captured: %d bytes", buf.Len())
	return buf.Bytes(), nil
}

func handleScreenshotRequest(payload []byte) {
	log.Println("[BINARY] Processing screenshot request...")

	screenshot, err := captureScreenshot()
	if err != nil {
		log.Printf("[BINARY] Screenshot error: %v", err)
		errorMsg := fmt.Sprintf("ERROR: %v", err)
		if err := sendBinaryData("SCRN", []byte(errorMsg)); err != nil {
			log.Printf("[BINARY] Failed to send error: %v", err)
		}
		return
	}

	if err := sendBinaryData("SCRN", screenshot); err != nil {
		log.Printf("[BINARY] Failed to send screenshot: %v", err)
	} else {
		log.Printf("[BINARY] Screenshot sent: %d bytes", len(screenshot))
	}
}

// === MAIN FUNCTION ===
func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("🚀 RMM Agent v2.0 - Phase 2: Fixed Quality Management")

	config := getCurrentQualityConfig()
	log.Printf("Initial quality: %s (%dx%d)", currentQuality, config.Width, config.Height)
	log.Printf("Connecting to: %s", serverURL)

	for i := 0; i < websocketMaxRetries; i++ {
		log.Printf("Connection attempt %d/%d...", i+1, websocketMaxRetries)
		err := runAgent()
		if err == nil {
			log.Println("Agent stopped gracefully")
			break
		}
		log.Printf("Error: %v. Retrying in %v...", err, websocketRetryDelay)
		time.Sleep(websocketRetryDelay)
	}

	log.Printf("Exiting after %d failed attempts", websocketMaxRetries)
	os.Exit(1)
}

func runAgent() error {
	ws, _, err := websocket.DefaultDialer.Dial(serverURL, nil)
	if err != nil {
		return fmt.Errorf("websocket connect error: %w", err)
	}
	defer ws.Close()
	log.Println("[WS] Connected to signaling server")

	writeChan := make(chan []byte, 100)
	go func() {
		for msg := range writeChan {
			err := ws.WriteMessage(websocket.TextMessage, msg)
			if err != nil {
				log.Printf("[WS] Write error: %v", err)
				return
			}
		}
	}()

	pcs := make(map[string]*webrtc.PeerConnection)
	var pcsLock sync.Mutex

	videoTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264},
		"video", "rmm",
	)
	if err != nil {
		return fmt.Errorf("track create error: %w", err)
	}

	config := getCurrentQualityConfig()
	log.Printf("[FFMPEG] Starting with quality: %s (%dx%d)", currentQuality, config.Width, config.Height)
	go manageFFmpegProcess(videoTrack)
	startVideoStats()

	// Main WebRTC signaling loop
	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Println("[WS] Closed cleanly")
				return nil
			}
			return fmt.Errorf("websocket read error: %w", err)
		}

		if handleSDP(msg, writeChan, pcs, &pcsLock, videoTrack) {
			continue
		}
		handleICE(msg, pcs, &pcsLock)
	}
}

// === MESSAGE HANDLING ===
func handleControlMessage(data []byte) {
	var ctl map[string]interface{}
	if err := json.Unmarshal(data, &ctl); err != nil {
		log.Printf("[CONTROL] Invalid JSON: %v", err)
		return
	}

	msgType, _ := ctl["type"].(string)
	log.Printf("[CONTROL] Received: %s", msgType)

	switch msgType {
	case "request_video_info":
		sendVideoInfo()

	case "video_dimensions_confirmed":
		width, _ := ctl["width"].(float64)
		height, _ := ctl["height"].(float64)
		log.Printf("[CONTROL] Browser confirmed dimensions: %.0fx%.0f", width, height)

	// === PHASE 2: QUALITY CHANGE HANDLING ===
	case "change_quality":
		quality, ok := ctl["quality"].(string)
		if !ok {
			log.Printf("[CONTROL] Invalid quality change request")
			return
		}

		log.Printf("[QUALITY] Received change request: %s", quality)

		// Validate quality
		if _, exists := QUALITY_PRESETS[quality]; !exists {
			log.Printf("[QUALITY] Unknown quality preset: %s", quality)
			return
		}

		// Update current quality
		oldQuality := currentQuality
		setCurrentQuality(quality)

		if oldQuality != currentQuality {
			log.Printf("[QUALITY] Quality changed: %s -> %s", oldQuality, currentQuality)

			// Restart FFmpeg with new settings
			select {
			case ffmpegRestartSignal <- struct{}{}:
				log.Printf("[QUALITY] FFmpeg restart signal sent")
			default:
				log.Printf("[QUALITY] FFmpeg restart signal already pending")
			}

			// Send confirmation
			config := getCurrentQualityConfig()
			confirmation := map[string]interface{}{
				"type":    "quality_changed",
				"quality": currentQuality,
				"width":   config.Width,
				"height":  config.Height,
				"fps":     config.FPS,
			}

			if err := sendControlMessage(confirmation); err != nil {
				log.Printf("[QUALITY] Failed to send confirmation: %v", err)
			}

			// Send updated video info
			go func() {
				time.Sleep(2 * time.Second) // Wait for FFmpeg to restart
				sendVideoInfo()
			}()
		}

	case "ping":
		pong := map[string]interface{}{
			"type": "pong",
		}
		if err := sendControlMessage(pong); err != nil {
			log.Printf("[CONTROL] Failed to send pong: %v", err)
		}

	case "mouse_move":
		x, okX := ctl["x"].(float64)
		y, okY := ctl["y"].(float64)
		if !okX || !okY {
			log.Printf("[CONTROL] Invalid mouse_move coordinates")
			return
		}

		// PHASE 2: Dynamic coordinate mapping based on current quality
		config := getCurrentQualityConfig()
		safeX := clampInt(int(x), 0, config.Width-1)
		safeY := clampInt(int(y), 0, config.Height-1)
		robotgo.MoveMouse(safeX, safeY)

	case "mouse_down", "mouse_up":
		btnF, ok := ctl["button"].(float64)
		if !ok {
			log.Printf("[CONTROL] Invalid mouse button")
			return
		}
		btn := int(btnF)
		names := []string{"left", "middle", "right"}
		if btn < 0 || btn >= len(names) {
			log.Printf("[CONTROL] Unknown mouse button: %d", btn)
			return
		}
		if msgType == "mouse_down" {
			robotgo.MouseDown(names[btn])
		} else {
			robotgo.MouseUp(names[btn])
		}

	case "key_down":
		keyStr, ok := ctl["key"].(string)
		if !ok {
			log.Printf("[CONTROL] Missing key field")
			return
		}
		robotgo.KeyDown(keyStr)

	case "key_up":
		keyStr, ok := ctl["key"].(string)
		if !ok {
			log.Printf("[CONTROL] Missing key field")
			return
		}
		robotgo.KeyUp(keyStr)

	default:
		log.Printf("[CONTROL] Unhandled event: %s", msgType)
	}
}

func handleBinaryMessage(data []byte) {
	if len(data) < 4 {
		log.Printf("[BINARY] Message too short: %d bytes", len(data))
		return
	}

	msgType := string(data[:4])
	payload := data[4:]

	log.Printf("[BINARY] Received: '%s', %d bytes", msgType, len(payload))

	switch strings.TrimSpace(msgType) {
	case "SCRN":
		handleScreenshotRequest(payload)
	default:
		log.Printf("[BINARY] Unknown message type: '%s'", msgType)
	}
}

// === FFMPEG MANAGEMENT WITH DYNAMIC QUALITY ===
func getFFmpegArgs() []string {
	config := getCurrentQualityConfig()

	var args []string
	if runtime.GOOS == "windows" {
		args = []string{"-f", "gdigrab", "-framerate", strconv.Itoa(config.FPS), "-draw_mouse", "1", "-i", "desktop"}
	} else {
		args = []string{"-f", "x11grab", "-framerate", strconv.Itoa(config.FPS), "-draw_mouse", "1", "-i", ":0.0"}
	}

	// PHASE 2: Dynamic resolution based on current quality
	args = append(args, "-s", fmt.Sprintf("%dx%d", config.Width, config.Height))

	args = append(args,
		"-vcodec", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-pix_fmt", "yuv420p", "-g", "60", "-keyint_min", "30",
		"-b:v", config.Bitrate, "-maxrate", config.Maxrate, "-bufsize", config.Bufsize,
		"-fflags", "nobuffer", "-f", "h264", "-",
	)

	return args
}

func manageFFmpegProcess(videoTrack *webrtc.TrackLocalStaticSample) {
	for {
		config := getCurrentQualityConfig()
		log.Printf("[FFMPEG] Starting new process with quality: %s (%dx%d)", currentQuality, config.Width, config.Height)

		quitSignal := make(chan struct{})

		ffmpegMutex.Lock()
		if currentFFmpegCmd != nil && currentFFmpegCmd.Process != nil {
			log.Println("[FFMPEG] Terminating previous process")
			_ = currentFFmpegCmd.Process.Kill()
		}
		ffmpegMutex.Unlock()

		args := getFFmpegArgs()
		log.Printf("[FFMPEG] Command: ffmpeg %s", strings.Join(args, " "))

		cmd := exec.Command("ffmpeg", args...)

		ffmpegMutex.Lock()
		currentFFmpegCmd = cmd
		ffmpegMutex.Unlock()

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			log.Printf("[FFMPEG] Stdout pipe error: %v", err)
			time.Sleep(5 * time.Second)
			close(quitSignal)
			continue
		}

		stderr, err := cmd.StderrPipe()
		if err != nil {
			log.Printf("[FFMPEG] Stderr pipe error: %v", err)
			_ = stdout.Close()
			time.Sleep(5 * time.Second)
			close(quitSignal)
			continue
		}
		go func() { io.Copy(io.Discard, stderr) }()

		if err = cmd.Start(); err != nil {
			log.Printf("[FFMPEG] Start error: %v", err)
			_ = stdout.Close()
			_ = stderr.Close()
			time.Sleep(5 * time.Second)
			close(quitSignal)
			continue
		}

		log.Printf("[FFMPEG] Process started successfully with quality: %s", currentQuality)

		var wg sync.WaitGroup
		wg.Add(1)

		go func() {
			defer wg.Done()
			streamVideo(stdout, videoTrack, quitSignal)
		}()

		ffmpegDone := make(chan struct{})
		go func() {
			defer close(ffmpegDone)
			err := cmd.Wait()
			if err != nil {
				log.Printf("[FFMPEG] Process exited with error: %v", err)
			} else {
				log.Println("[FFMPEG] Process exited normally")
			}
			close(quitSignal)
		}()

		select {
		case <-ffmpegRestartSignal:
			log.Println("[FFMPEG] Restart signal received")
			ffmpegMutex.Lock()
			if cmd != nil && cmd.Process != nil {
				if runtime.GOOS == "windows" {
					_ = cmd.Process.Kill()
				} else {
					if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
						_ = cmd.Process.Kill()
					}
				}
			}
			ffmpegMutex.Unlock()
			select {
			case ffmpegStatsReset <- struct{}{}:
			default:
			}
		case <-ffmpegDone:
			log.Println("[FFMPEG] Process lifecycle completed")
		}

		wg.Wait()
		<-ffmpegDone
		time.Sleep(1 * time.Second)
	}
}

func streamVideo(r io.Reader, videoTrack *webrtc.TrackLocalStaticSample, quit <-chan struct{}) {
	reader := bufio.NewReader(r)
	const maxNALUBufferSize = 2 * 1024 * 1024
	buf := make([]byte, 0, maxNALUBufferSize)
	tmp := make([]byte, 4096)

	config := getCurrentQualityConfig()

	for {
		select {
		case <-quit:
			log.Println("[FFMPEG] Video streaming stopped")
			return
		default:
			n, err := reader.Read(tmp)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					log.Printf("[FFMPEG] Read error: %v", err)
				}
				return
			}

			if len(buf)+n > maxNALUBufferSize {
				log.Printf("[FFMPEG] Buffer overflow, resetting")
				buf = buf[:0]
				continue
			}
			buf = append(buf, tmp[:n]...)

			for {
				start := findStartCode(buf)
				if start == -1 {
					break
				}

				next := findStartCode(buf[start+4:])
				if next == -1 {
					break
				}
				next += start + 4

				nalu := buf[start:next]
				if len(nalu) == 0 {
					buf = buf[next:]
					continue
				}

				select {
				case <-quit:
					return
				default:
					_ = videoTrack.WriteSample(media.Sample{
						Data:     nalu,
						Duration: time.Second / time.Duration(config.FPS),
					})

					videoStatsLock.Lock()
					videoBytesSent += int64(len(nalu))
					videoFramesSent++
					videoStatsLock.Unlock()
				}
				buf = buf[next:]
			}
		}
	}
}

func findStartCode(data []byte) int {
	for i := 0; i < len(data)-3; i++ {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
			return i
		}
	}
	return -1
}

// === WebRTC SETUP ===
func newPeerConnection(out chan []byte, videoTrack *webrtc.TrackLocalStaticSample) (*webrtc.PeerConnection, error) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{},
	})
	if err != nil {
		return nil, err
	}

	if _, err := pc.AddTrack(videoTrack); err != nil {
		log.Printf("[WebRTC] AddTrack error: %v", err)
	}

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		channelManager.mutex.Lock()
		defer channelManager.mutex.Unlock()

		switch dc.Label() {
		case "control":
			channelManager.controlChannel = dc
			setupControlChannel(dc)
			log.Println("[DATACHANNEL] Control channel established")

		case "binary":
			channelManager.binaryChannel = dc
			setupBinaryChannel(dc)
			log.Println("[DATACHANNEL] Binary channel established")

		default:
			log.Printf("[DATACHANNEL] Unknown channel: %s", dc.Label())
		}
	})

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			if payload, err := json.Marshal(c.ToJSON()); err == nil {
				out <- payload
			}
		}
	})

	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		log.Printf("[WebRTC] Connection state: %s", s.String())
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			log.Printf("[WebRTC] Connection %s, detaching channels", s.String())
			channelManager.mutex.Lock()
			channelManager.controlChannel = nil
			channelManager.binaryChannel = nil
			channelManager.mutex.Unlock()
		}
	})

	return pc, nil
}

// === DATACHANNEL SETUP ===
func setupControlChannel(dc *webrtc.DataChannel) {
	dc.OnOpen(func() {
		log.Println("[CONTROL] DataChannel opened")
		sendVideoInfo() // Send current video dimensions
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		handleControlMessage(msg.Data)
	})

	dc.OnClose(func() {
		log.Println("[CONTROL] DataChannel closed")
		channelManager.mutex.Lock()
		channelManager.controlChannel = nil
		channelManager.mutex.Unlock()
	})

	dc.OnError(func(err error) {
		log.Printf("[CONTROL] DataChannel error: %v", err)
	})
}

func setupBinaryChannel(dc *webrtc.DataChannel) {
	dc.OnOpen(func() {
		log.Println("[BINARY] DataChannel opened")
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		handleBinaryMessage(msg.Data)
	})

	dc.OnClose(func() {
		log.Println("[BINARY] DataChannel closed")
		channelManager.mutex.Lock()
		channelManager.binaryChannel = nil
		channelManager.mutex.Unlock()
	})

	dc.OnError(func(err error) {
		log.Printf("[BINARY] DataChannel error: %v", err)
	})
}

// === SDP/ICE HANDLING ===
func handleSDP(msg []byte, out chan []byte, pcs map[string]*webrtc.PeerConnection,
	lock *sync.Mutex, videoTrack *webrtc.TrackLocalStaticSample) bool {

	var sdp webrtc.SessionDescription
	if err := json.Unmarshal(msg, &sdp); err != nil || sdp.Type != webrtc.SDPTypeOffer {
		return false
	}

	lock.Lock()
	if old, ok := pcs["viewer"]; ok {
		log.Println("[WebRTC] Closing old PeerConnection")
		_ = old.Close()
	}
	pc, err := newPeerConnection(out, videoTrack)
	if err != nil {
		lock.Unlock()
		log.Printf("[WebRTC] PeerConnection error: %v", err)
		return true
	}
	pcs["viewer"] = pc
	lock.Unlock()

	_ = pc.SetRemoteDescription(sdp)

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		log.Printf("[WebRTC] CreateAnswer error: %v", err)
		return true
	}
	_ = pc.SetLocalDescription(answer)
	payload, _ := json.Marshal(answer)
	out <- payload

	return true
}

func handleICE(msg []byte, pcs map[string]*webrtc.PeerConnection, lock *sync.Mutex) {
	var ice webrtc.ICECandidateInit
	if err := json.Unmarshal(msg, &ice); err != nil || ice.Candidate == "" {
		return
	}
	lock.Lock()
	defer lock.Unlock()
	for _, pc := range pcs {
		if pc.RemoteDescription() != nil {
			err := pc.AddICECandidate(ice)
			if err != nil {
				log.Printf("[WebRTC] AddICECandidate error: %v", err)
			}
		}
	}
}

// === UTILITY FUNCTIONS ===
func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func startVideoStats() {
	go func() {
		var prevBytes int64
		var prevFrames int64
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ffmpegStatsReset:
				videoStatsLock.Lock()
				prevBytes = videoBytesSent
				prevFrames = videoFramesSent
				videoStatsLock.Unlock()
				log.Println("[STATS] Counters reset")
			case <-ticker.C:
				videoStatsLock.Lock()
				bytesDelta := videoBytesSent - prevBytes
				framesDelta := videoFramesSent - prevFrames
				prevBytes = videoBytesSent
				prevFrames = videoFramesSent
				videoStatsLock.Unlock()

				fps := float64(framesDelta) / 5.0
				mbps := float64(bytesDelta) * 8 / 1_000_000 / 5.0

				config := getCurrentQualityConfig()
				log.Printf("[STATS] Quality: %s | FPS: %.1f | Mbps: %.2f | Resolution: %dx%d | Frames: %d | Total: %s",
					currentQuality, fps, mbps, config.Width, config.Height, framesDelta, formatBytes(videoBytesSent))
			}
		}
	}()
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
```

Ключевые улучшения в Фазе 2 🚀
✅ Добавленная функциональность:
Адаптивное качество видео

720p/1080p/1440p presets с оптимизированными настройками
Автоматическое переключение на основе latency
UI controls для ручного выбора качества
Кэширование координат

Кэш coordinate mapping для избежания DOM запросов
Cache hits tracking для оптимизации
Расширенная производительность

Performance monitoring panel
Отслеживание FPS, bandwidth, latency
Mouse events per second tracking
Улучшенная коммуникация

Timestamp в сообщениях для измерения latency
Enhanced ping/pong с performance stats
Quality change confirmations
Оптимизированная видеопередача

Динамические настройки FFmpeg на основе качества
Адаптивный frame duration
Улучшенная статистика
🎯 Производительные оптимизации:
Throttling: Улучшен до 8ms (125fps) для лучшей отзывчивости
Caching: Coordinate mapping кэшируется на 100ms
Auto Quality: Автоматическое понижение/повышение качества
Stats: Расширенная статистика производительности
📊 Новые UI элементы:
Quality Selector: Dropdown с presets + Auto mode
Quality Indicator: Показывает текущее разрешение/FPS/ping
Performance Panel: Детальная информация о производительности
Enhanced Debug: Улучшенная debug информация с кэшированием
Теперь у вас есть полноценная система с адаптивным качеством и мониторингом производительности!

Протестировал:

1. Меняю в браузере:

исходные в браузере и на агенте 1080p
PHASE 2 DEBUG - OPTIMIZED
Video Native: 1920x1080
Element Size: 1917x973
Draw Area: 1730x973

в браузере 1440p, на агенте 1080p
PHASE 2 DEBUG - OPTIMIZED
Video Native: 2560x1440
Element Size: 1917x973
Draw Area: 1730x973

Получаю ошибку в позиции мыши.
Окно в браузере не увеличилось.

2.  исходные в браузере и на агенте 1080p
    PHASE 2 DEBUG - OPTIMIZED
    Video Native: 1920x1080
    Element Size: 1917x973
    Draw Area: 1730x973

Меняю на агенте до 720p, в браузер пока 1080p

получаю окно в окне, ошибок позиции мыши нет.

Меняю в браузере на 720p
PHASE 2 DEBUG - OPTIMIZED
Video Native: 1280x720
Element Size: 1917x973
Draw Area: 1730x973
Окно расширяется, исчезает эффект окно в окне, но появилась ошибка позиции мыши.
