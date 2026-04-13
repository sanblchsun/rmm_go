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

| Действие | Описание |
|----------|----------|
| Мышь | Движение, клики (левая/средняя/правая кнопка) |
| Клавиатура | Все клавиши + русский ввод |
| Ctrl+Shift | Переключение EN/RU индикатора |

## Устранение проблем

**FFmpeg не найден:** Добавьте FFmpeg в PATH или положите рядом с agent.exe

**Нет видео:** Проверьтеfirewall - порт 8000 должен быть открыт

**Тормозит:** Уменьшите fps в `main.go` строка 276: `"-framerate", "30"`