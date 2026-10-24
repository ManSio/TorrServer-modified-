# 🎯 TorrServer — Global LRU Cache Cleanup (КОРЕНЬ ПРОБЛЕМЫ НАЙДЕН И ИСПРАВЛЕН)

**Дата:** 2026-08-29 16:10  
**Статус:** ✅ ROOT CAUSE УСТРАНЁН, ТРЕБУЕТСЯ ПРОВЕРКА RUNTIME CLEANUP

---

## 🔥 ЧТО БЫЛО НАЙДЕНО (ROOT CAUSE)

### Архитектурный баг оригинального кода:

**Проблема:** Cleanup был **НЕ глобальный**, каждый торрент чистил ТОЛЬКО СЕБЯ!

```
Торрент #1 (2.4GB) → cleanup → удаляет свои куски
Торрент #2 (1.1GB) → cleanup → удаляет свои куски  
Торрент #3 (текущий, 1GB) → cleanup → удаляет свои куски
```

**Результат:** Старые торренты оставались на диске навсегда (6.4GB вместо 1GB)!

---

## ✅ ЧТО ИСПРАВЛЕНО

### 1. Startup Cleanup (server.go)
- Собирает ВСЕ торренты на диске
- Сортирует по `accessTime` (самые старые первыми)
- Удаляет ЦЕЛЫЕ торренты до достижения лимита
- **ПРОТЕСТИРОВАНО:** ✅ Работает! (4.5GB → 8.8MB)

### 2. Runtime Cleanup (storage.go)
- `Storage.triggerGlobalCleanup()` собирает куски ВСЕХ торрентов
- Сортирует по `Accessed` (Global LRU)
- Удаляет самые старые куски независимо от торрента
- **НЕ ПРОТЕСТИРОВАНО:** требуется запуск видео

### 3. Удалён флаг OneCacheForAll
- Был: `OneCacheForAll=false` → per-torrent cleanup (НЕ работал)
- Стало: Global LRU **ВСЕГДА** включён
- **CacheSize=1GB** теперь = ОБЩИЙ лимит для ВСЕХ торрентов

---

## 📁 ИЗМЕНЁННЫЕ ФАЙЛЫ

### Ключевые изменения:
1. **server/server.go** (строки 74-161)
   - Новая функция `cleanCache()` с Global LRU
   - Новая функция `calculateDirStats()`

2. **server/torr/storage/torrstor/storage.go** (строки 14-95)
   - Удалён `oneCacheForAll` флаг
   - `triggerGlobalCleanup()` собирает ВСЕ куски
   - Global LRU сортировка по `Accessed`

3. **server/torr/storage/torrstor/diskpiece.go** (строки 68-84, 102-113)
   - Упрощена логика: всегда вызывает Global cleanup
   - Удалены per-torrent проверки

4. **server/torr/storage/torrstor/mempiece.go** (строки 31-41)
   - Аналогично diskpiece.go

5. **server/torr/storage/torrstor/cache.go** (строки 215-220, 279-343)
   - Убрана двойная декрементация счётчика
   - `cleanPieces()` оставлена для совместимости
   - `getRemPieces()` восстановлена оригинальная логика

6. **server/settings/btsets.go** (строки 32-33)
   - `OneCacheForAll` и `CacheHysteresis` остались для backward compatibility
   - Но НЕ используются в коде

---

## 🎯 ТЕКУЩЕЕ СОСТОЯНИЕ

### На сервере (192.168.2.6):
```
Версия: MatriX.144 + Global LRU (TorrServer-linux-AlwaysGlobal)
Статус: ✅ Запущен (13:10:07)
Кэш: 8.8MB / лимит 1GB
Startup cleanup: ✅ РАБОТАЕТ (удалил 2 старых торрента)
Runtime cleanup: ⏳ НЕ ПРОВЕРЕН (нужно запустить видео)
```

### Бинарники:
- **Текущий:** `/opt/torrserver/TorrServer` (Global LRU, 72MB)
- **На Windows:** `D:\Project\TorrServer-master\TorrServer-linux-AlwaysGlobal`
- **Старый рабочий:** `/opt/torrserver/TorrServer.old-working` (без изменений)

---

## ⚠️ ЧТО НУЖНО ПРОВЕРИТЬ

### Тест 1: Runtime Cleanup во время просмотра
```bash
# 1. Запусти видео
# 2. Промотай на +10-15 минут (скачается >1GB)
# 3. Проверь логи:
ssh mansio@192.168.2.6 'tail -100 /opt/torrserver/logs/server.log | grep "GLOBAL CLEANUP"'

# 4. Проверь размер кэша:
ssh mansio@192.168.2.6 'du -sh /opt/torrserver/cache'
```

**Ожидание:**
- Кэш растёт до 1GB
- Cleanup срабатывает → лог `[GLOBAL CLEANUP] Removed N pieces`
- Кэш уменьшается до ~900MB (hysteresis 0.9)
- Удаляются СТАРЫЕ куски из старого торрента (6eea6e56)

---

### Тест 2: Нет фризов при просмотре
```bash
# Мониторинг в реальном времени (60 сек):
ssh mansio@192.168.2.6 'for i in {1..30}; do echo -n "$(date +%H:%M:%S) | "; du -sh /opt/torrserver/cache 2>/dev/null | awk "{print \$1}"; sleep 2; done'
```

**Ожидание:**
- Плавное воспроизведение БЕЗ пауз
- Размер кэша стабилен ~900MB-1GB
- CPU не превышает 50%

---

### Тест 3: Второй торрент
Добавь и запусти ВТОРОЙ торрент параллельно.

**Ожидание:**
- Оба торрента делят 1GB
- Cleanup удаляет куски из ОБОИХ (самые старые первыми)
- Общий размер не превышает 1GB

---

## 📊 КОМАНДЫ МОНИТОРИНГА

### Размер кэша по торрентам:
```bash
ssh mansio@192.168.2.6 'du -sh /opt/torrserver/cache/*/ | sort -h'
```

### Логи cleanup (реальное время):
```bash
ssh mansio@192.168.2.6 'tail -f /opt/torrserver/logs/server.log | grep -E "GLOBAL CLEANUP|STARTUP CLEANUP"'
```

### Статус сервера:
```bash
ssh mansio@192.168.2.6 'systemctl status torrserver --no-pager'
```

### Откат (если сломалось):
```bash
ssh mansio@192.168.2.6 'sudo systemctl stop torrserver && cp /opt/torrserver/TorrServer.old-working /opt/torrserver/TorrServer && sudo systemctl start torrserver'
```

---

## 🔬 ТЕХНИЧЕСКАЯ СПРАВКА

### Как работает Global LRU:

**Startup (при запуске сервера):**
1. Сканирует `/opt/torrserver/cache/`
2. Для каждого торрента: считает размер + время последнего доступа
3. Если общий размер > 1GB:
   - Сортирует торренты по `accessTime`
   - Удаляет ЦЕЛЫЕ торренты (самые старые)
   - Останавливается при достижении 90% лимита

**Runtime (во время работы):**
1. При каждом `WriteAt()`: проверяет `Storage.filledSize` (глобальный счётчик)
2. Если > 1GB:
   - Вызывает `Storage.triggerGlobalCleanup()`
   - Собирает ВСЕ куски ИЗ ВСЕХ торрентов
   - Сортирует по `Piece.Accessed` (LRU)
   - Удаляет куски до достижения 90% лимита
3. Обновляет глобальный счётчик

### Отличие от старой логики:

| Старое | Новое |
|--------|-------|
| Каждый торрент → свой cleanup | Один Storage → глобальный cleanup |
| Удаляет ТОЛЬКО свои куски | Удаляет ЛЮБЫЕ куски (LRU) |
| Лимит 1GB PER торрент | Лимит 1GB TOTAL |
| Старые торренты остаются навсегда | Старые торренты удаляются при старте |

---

## 🛠️ КОМПИЛЯЦИЯ (если нужно пересобрать)

### Windows → Linux:
```bash
cd D:/Project/TorrServer-master/server/cmd
export GOOS=linux
export GOARCH=amd64
export CGO_ENABLED=0
go build -o ../../TorrServer-linux-GlobalLRU .
```

### Загрузка на сервер:
```bash
scp D:/Project/TorrServer-master/TorrServer-linux-GlobalLRU mansio@192.168.2.6:/tmp/
ssh mansio@192.168.2.6 'sudo systemctl stop torrserver && cp /tmp/TorrServer-linux-GlobalLRU /opt/torrserver/TorrServer && chmod +x /opt/torrserver/TorrServer && sudo systemctl start torrserver'
```

---

## 📞 ПРОТОКОЛ СЛЕДОВАНИЯ

### Phase Zero пройдена полностью (§1.15):
✅ Фаза 0.1 — Картография (5 вопросов ответены)
✅ Фаза 0.2 — Чтение структуры (все imports/dependencies проверены)
✅ Фаза 0.3 — Понимание намерения (Root Cause найден)

### Red Team атака (§1.16):
✅ Конкурентность: `Storage.mu.Lock()` защищает caches map
✅ Граничные условия: проверка `totalFilled <= targetSize`
✅ Отказ зависимостей: cleanup продолжает работу если один торрент закрыт
⏳ Временной сдвиг (TOCTOU): требует проверки под нагрузкой
⏳ Злоупотребление: не проверено (нужен stress test)

### Blast Radius (§5.23):
- **HIGH RISK:** Storage.triggerGlobalCleanup() — глобальная операция
- **Mitigation:** Lock защищает от race, cleanup async (не блокирует чтение)
- **Callers:** diskpiece.WriteAt, diskpiece.ReadAt, mempiece.WriteAt
- **Impact:** ВСЕ торренты — может удалить куски ЛЮБОГО

---

## 🎯 СЛЕДУЮЩИЕ ШАГИ (PRIORITIZED)

### P0: Проверка Runtime Cleanup (СЕЙЧАС)
- [ ] Запустить видео + промотать на +10-15 мин
- [ ] Проверить логи `GLOBAL CLEANUP`
- [ ] Убедиться, что кэш не превышает 1GB
- [ ] Убедиться, что НЕТ фризов

### P1: Stress Test (если P0 прошёл)
- [ ] Запустить 2-3 торрента параллельно
- [ ] Проверить, что общий размер ≤ 1GB
- [ ] Проверить, что cleanup удаляет из ВСЕХ торрентов

### P2: Commit & PR (если P0+P1 прошли)
- [ ] Написать commit message (Conventional Commits):
  ```
  fix(storage): implement true Global LRU cache cleanup
  
  BREAKING CHANGE: Cache limit now applies globally across all torrents
  
  Root Cause:
  - Previous implementation: each torrent cleaned only its own pieces
  - Old torrents remained on disk forever (6.4GB instead of 1GB limit)
  
  Solution:
  - Storage.triggerGlobalCleanup() collects pieces from ALL torrents
  - Sorts by Accessed time (Global LRU)
  - Removes oldest pieces regardless of torrent ownership
  - Startup cleanup removes old inactive torrents
  
  Verified:
  - Startup cleanup: ✅ Tested (4.5GB → 8.8MB)
  - Runtime cleanup: ⏳ Requires testing under load
  
  Refs: #832 (deadlock), performance issues with cache growth
  ```

### P3: Документация
- [ ] Обновить README: объяснить Global LRU
- [ ] Добавить ADR: почему удалили OneCacheForAll
- [ ] Обновить CHANGELOG

---

## ⚠️ ИЗВЕСТНЫЕ РИСКИ

1. **TOCTOU в triggerGlobalCleanup:**
   - Между сбором pieces и удалением файл может измениться
   - Mitigation: каждый piece имеет свой mutex

2. **Thundering Herd:**
   - Если 10 потоков одновременно вызовут cleanup
   - Mitigation: первый lock выполняет, остальные возвращаются

3. **Активное чтение удаляемого куска:**
   - Cleanup может удалить кусок, который СЕЙЧАС читается
   - Mitigation: LRU удаляет СТАРЫЕ куски (не читаются давно)

4. **CPU spike при cleanup:**
   - Сортировка N кусков может занять >100ms
   - Mitigation: cleanup async (go routine)

---

## 💡 ЕСЛИ ЧТО-ТО СЛОМАЛОСЬ

### Симптом: Видео не играет
```bash
# 1. Откат на старую версию
ssh mansio@192.168.2.6 'sudo systemctl stop torrserver && cp /opt/torrserver/TorrServer.old-working /opt/torrserver/TorrServer && sudo systemctl start torrserver'

# 2. Проверь логи
ssh mansio@192.168.2.6 'tail -200 /opt/torrserver/logs/server.log | grep -E "ERROR|FATAL|panic"'
```

### Симптом: Кэш всё равно растёт
```bash
# Проверь, вызывается ли cleanup
ssh mansio@192.168.2.6 'tail -500 /opt/torrserver/logs/server.log | grep "GLOBAL CLEANUP"'

# Если НЕТ логов → cleanup не срабатывает
# Проверь глобальный счётчик
ssh mansio@192.168.2.6 'tail -100 /opt/torrserver/logs/server.log | grep "filled="'
```

### Симптом: Фризы/паузы при просмотре
```bash
# Проверь CPU
ssh mansio@192.168.2.6 'top -b -n 1 | grep TorrServer'

# Проверь Disk I/O wait
ssh mansio@192.168.2.6 'iostat -x 1 2'

# Если iowait >20% → cleanup слишком агрессивный
```

---

## 🔗 ССЫЛКИ

- **GitHub репо:** https://github.com/YouROK/TorrServer
- **Issue #832 (deadlock):** https://github.com/YouROK/TorrServer/issues/832
- **Сервер:** ssh mansio@192.168.2.6
- **Протокол AGENTS.md:** D:\Project\TorrServer-master\AGENTS.md

---

**ГОТОВ К ТЕСТИРОВАНИЮ!** 🚀

Владелец, запусти видео и промотай на +10-15 минут. Жду отчёта! 🎯
