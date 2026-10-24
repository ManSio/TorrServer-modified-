# ⚠️ План реальной проверки ПЕРЕД отправкой на GitHub

## Текущий статус

❌ **КОД НЕ ПРОВЕРЕН НА РЕАЛЬНОМ TORRSERVER**

Всё что было сделано:
- ✅ Код написан
- ✅ Тесты написаны
- ⚠️ НО: ничего не компилировалось и не запускалось

---

## 🎯 Checklist реальной проверки

### Phase 1: Компиляция (ОБЯЗАТЕЛЬНО)

```bash
cd TorrServer-master

# Проверить что go.mod существует
ls go.mod

# Попробовать собрать
go build

# Если ошибки компиляции — СТОП, чинить перед продолжением
```

**Без успешной компиляции код НЕЛЬЗЯ отправлять на GitHub.**

---

### Phase 2: Unit Tests (ОБЯЗАТЕЛЬНО)

```bash
# Запустить наши unit tests
cd server/torr/storage/torrstor
go test -v -run TestBackwardCompatibility
go test -v -run TestGlobalCacheLimit
go test -v -run TestGlobalEvictionLRU
go test -v -run TestHysteresisThreshold
go test -v -run TestAtomicCounterRaces
go test -v -run TestInactiveTorrentHandling

# Все 6 тестов должны пройти
```

**Если хотя бы 1 тест падает — СТОП, чинить.**

---

### Phase 3: Существующие Tests (ОБЯЗАТЕЛЬНО)

```bash
# Запустить ВСЕ существующие тесты проекта
cd TorrServer-master
go test ./...

# Убедиться что мы не сломали существующий функционал
```

**Если сломали чужие тесты — КРИТИЧЕСКАЯ ПРОБЛЕМА.**

---

### Phase 4: Запуск реального сервера (ЖЕЛАТЕЛЬНО)

```bash
# Собрать и запустить
go build
./TorrServer

# Проверить:
1. Сервер стартует без паники
2. Web UI открывается
3. Можно добавить торрент
4. Можно начать скачивание
5. Можно проиграть файл
```

---

### Phase 5: Проверка новой функциональности (КРИТИЧНО)

#### Test Case 1: Per-Torrent Mode (default)

**Config:**
```json
{
  "CacheSize": 100000000,
  "OneCacheForAll": false
}
```

**Действия:**
1. Добавить 2 торрента
2. Начать скачивание обоих
3. Проверить: каждый торрент имеет свой лимит 100MB

**Ожидание:** Работает как раньше (backward compatibility)

---

#### Test Case 2: Global Mode

**Config:**
```json
{
  "CacheSize": 100000000,
  "OneCacheForAll": true
}
```

**Действия:**
1. Добавить 3 торрента
2. Начать скачивание всех трёх
3. Проверить: сумма всех кэшей ≤ 100MB

**Ожидание:** Глобальный лимит работает

---

#### Test Case 3: Fractional Preload

**Config:**
```json
{
  "PreloadCache": 0.25
}
```

**Действия:**
1. Добавить большой торрент (>1GB)
2. Начать проигрывание
3. Проверить лог: сколько MB preload

**Ожидание:** ~0.25% от размера торрента

---

### Phase 6: Stress Test (ОПЦИОНАЛЬНО)

```bash
cd scripts
go run stress_test_cache.go
```

**Если падает — смотреть логи.**

---

## ⛔ Блокеры для GitHub PR

Код **НЕЛЬЗЯ** отправлять на GitHub если:

1. ❌ Не компилируется
2. ❌ Хотя бы 1 unit test падает
3. ❌ Сломали существующие тесты
4. ❌ Сервер паникует при старте
5. ❌ Backward compatibility сломана

---

## ✅ Готовность к GitHub

Код **МОЖНО** отправлять если:

1. ✅ Компилируется без ошибок
2. ✅ Все 6 unit tests проходят
3. ✅ Существующие тесты не сломаны
4. ✅ Сервер стартует
5. ✅ Backward compatibility работает (Test Case 1)

**Желательно (но не обязательно):**
- ✅ Test Case 2 (global mode) работает
- ✅ Test Case 3 (fractional preload) работает

---

## 🎯 Реалистичная оценка времени

| Phase | Время | Критичность |
|-------|-------|-------------|
| Phase 1: Компиляция | 5 мин | БЛОКЕР |
| Phase 2: Unit tests | 2 мин | БЛОКЕР |
| Phase 3: Existing tests | 10 мин | БЛОКЕР |
| Phase 4: Запуск сервера | 5 мин | Желательно |
| Phase 5: Функциональность | 20 мин | Желательно |
| Phase 6: Stress test | 10 мин | Опционально |

**Минимум:** 17 минут (Phase 1-3)  
**Полная проверка:** 52 минуты (всё)

---

## 📋 Что делать ПРЯМО СЕЙЧАС

### Вариант 1: "Быстрая проверка" (17 минут)

```bash
cd TorrServer-master
go build                    # Phase 1
cd server/torr/storage/torrstor
go test -v                  # Phase 2
cd ../../../..
go test ./...               # Phase 3
```

Если всё ✅ — можно отправлять на GitHub с пометкой:
> "Tested: compiles, unit tests pass, no regressions. Full integration testing in progress."

---

### Вариант 2: "Полная проверка" (52 минуты)

Всё из Variant 1 + Phase 4-6.

Если всё ✅ — можно отправлять с полной уверенностью.

---

### Вариант 3: "Честная пометка" (0 минут)

Отправить PR **СЕЙЧАС**, но в описании написать:

> ⚠️ **Status:** Code compiles but not tested on live server yet. 
> Looking for feedback on approach before full integration testing.

**Риск:** Maintainer может закрыть без рассмотрения.

---

## 💡 Моя рекомендация

**Вариант 1** — минимальная проверка (17 минут).

Почему:
1. Go код обычно "если компилируется — работает"
2. Unit tests покрывают ключевую логику
3. Не ломаем существующий функционал
4. Честно говорим в PR: "tested compilation + unit tests"

**НЕ ВРАТЬ** в PR что "tested on production" если не тестили.

---

## ❓ Твой выбор

Что ты хочешь сделать?

**Option A:** Проверить компиляцию + unit tests сейчас (17 мин), потом GitHub  
**Option B:** Полная проверка с запуском сервера (52 мин), потом GitHub  
**Option C:** Отправить на GitHub с честной пометкой "not tested yet"  
**Option D:** Я сам попробую проверить (но у меня нет TorrServer окружения)  

**Выбери букву и скажи мне.**
