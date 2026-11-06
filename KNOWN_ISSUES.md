# KNOWN_ISSUES — TorrServer Cache Architecture

## KI-001: Dead Code — cleanPieces() (2026-08-29)
**Status:** 🟡 Наблюдаем
**File:** `server/torr/storage/torrstor/cache.go:278-330`
**Описание:** `cleanPieces()` — старый путь cleanup, не вызывается из v4 (`globalCleanupCheck` удалён). Функция содержит `muRemove.TryLock()` и `getRemPieces()` — сложная логика, которая может быть удалена или сохранена для backward compat.
**Deadline:** Не критично (не влияет на v4)
**Guard:** Не удалять без проверки, что `cleanPieces()` не вызывается из других модулей (`grep -rn "cleanPieces"`).

## KI-002: Lock Contention — Release() vs ReadAt() (2026-08-29) — ROOT CAUSE
**Status:** 🔴 Открыто (P0)
**File:** `diskpiece.go:85` (ReadAt Lock), `diskpiece.go:119` (Release Lock)
**Root Cause:** Оба метода используют `p.mu.Lock()` (RWMutex, но Lock — не RLock). Cleanup через `triggerGlobalCleanup()` → `removePiece()` → `Release()` блокирует `ReadAt()` на том же piece. Даже 15 pieces = 15 блокировок чтения.
**Fix (v4):** Уменьшить `maxRemovePerCycle` с 15 → 5 или 10; увеличить debounce с 1 → 2 сек; рассмотреть `RLock` для `ReadAt()` или async `Release()` без блокировки.
**Deadline:** До установки v4 на прод
**Guard:** Тест `TestRaceCondition_ConcurrentWrites` не проверяет ReadAt() во время cleanup — нужен новый тест.
**Уточнение 2026-10-24:** `maxRemovePerCycle` больше не участвует — вытеснение идёт байтовым бюджетом
(KI-003, `storage.go:836`). `ReadAt`/`Release` по-прежнему под `p.mu.Lock()` (`diskpiece.go:82`, `:112`),
поэтому статус P0 остаётся открытым; предложенный в записи фикс устарел.

## KI-003: Лимит кеша не применялся к данным удалённых из памяти торрентов (2026-10-01) — FIXED и РАЗВЁРНУТО на 192.168.2.6
**Status:** 🟢 Развёрнуто 2026-10-01 18:44 UTC, подтверждено на прод-данных
**Files:** `torrstor/storage.go` (orphanBytes, reconcileOrphans, evictOrphanDirs, evictPiecesTo),
`torrstor/cache.go` (releaseOnClose), `torrstor/reader.go` (getOffsetRange),
`torr/apihelper.go` (WriteCacheStatus → /stat)
**Root Cause (3 слоя, все воспроизведены):**
1. **Слепой учёт.** `Cache.Close()` убирал cache из `s.caches`, файлы оставались на диске
   (`RemoveCacheOnDrop=false`). `Torrent.expired()` делает это автоматически через
   `TorrentDisconnectTimeout` (120 с) после остановки просмотра. Байты выпадали из бюджета.
   Замер на прод: счётчик 8.6 GiB при 129.5 GiB на диске (22 каталога, 43 164 файла) —
   `runCleanupIfNeeded` выходил по первой строке, LRU не стартовал с 2026-08-29.
2. **Затирание счётчика.** `triggerGlobalCleanup` делал `filledSize.Store(сумма по открытым
   cache)`, выбрасывая байты закрытых. Тот же Store затирал конкурентные записи WriteAt
   (поймано тестом: записано 16 МБ, учтено 9 МБ).
3. **Окно защиты ридера масштабировалось от лимита.** `getOffsetRange()` = `cache.capacity *
   ReaderReadAHead%` → при 97.9 GiB и 81% один ридер защищал ~79 GiB, вытеснение не сходилось.
**Fix:** единый источник истины — диск (`filledSize` + `orphanBytes`, transfer при Close,
reconcile 5 мин / 60 с при превышении), вытеснение в 2 фазы (закрытые каталоги
coldest-first, затем куски открытых cache), окно защиты от readahead ридера вместо лимита,
`CacheHysteresis` из настроек, лог `[CACHE]` на каждый цикл.
**Измерения на живом сервере (2026-10-01):** полный скан кеша 43 164 файлов = **1.50 с**
(3 прогона: 1.495/1.483/1.520 с); unlink **0.23–0.33 мс/файл**, rmtree 4000 файлов = 0.92 с.
Поэтому `maxRemovePerCycle` заменён на бюджет в байтах (2 GiB + кап 2000 кусков): старые
200 кусков за цикл не успевали за скоростью скачивания (сброс 40 GiB ≈ 9 мин).
**Guard:** `cache_limit_test.go` (7 тестов, control + negative control на каждое),
`leak_repro_test.go` (4 теста на окно ридера), лог `[CACHE] OVER LIMIT and nothing
removable` (не чаще 5 мин), `WriteCacheStatus` в /stat.
**Проверено / НЕ проверено:** механизм вытеснения подтверждён на стенде (60 МБ → 0 при лимите
32 МБ, реальный бинарь). НЕ проверено на реальном торренте с ридером (фаза 2), НЕ проверен
race detector (нет gcc на машине), сценарий «каталог принадлежит торренту из списка» покрыт
только юнит-тестом и чтением кода (`reconcileOrphans` не обращается к списку торрентов).
**Факт развёртывания (прод-данные, 2026-10-01 18:44 UTC):**
- бинарь `ea6025c4…`; бэкап прежнего — `/opt/torrserver/TorrServer.bak-20261001-214408`
  (откат: `sudo mv TorrServer.bak-20261001-214408 TorrServer && sudo systemctl restart torrserver`)
- было 139 093 694 384 Б (129.5 GiB) при лимите 97.9 GiB → стало 89 904 315 511 Б (83.7 GiB = 85.5 %)
- освободилось 49.2 ГБ; каталогов 22 → 18, файлов 43 164 → 34 279; сходимость ~2 мин
- `/stat`: `Cache: 83.7 GiB of 97.9 GiB (85.5%) — in memory 0 B, not in memory 83.7 GiB`
- 0 panic / 0 error после рестарта; скан на проде 43 164 файлов = 3.37 с
- снесённые каталоги (докачать при следующем просмотре): `30f04742…` «Укрытие (Silo) Сезон 3»
  28 ГБ (удалён старым `cleanCache`, т.к. торрент не в списке), `685acda2…` 11.5 ГБ,
  `e9b48cbb…` 3.6 ГБ, `645c8db6…` 3.0 ГБ — последние три моей фазой 1
**Открытый вопрос владельцу:** `RemoveCacheOnDrop=false` теперь означает «данные переживут
drop, пока хватает бюджета», а не «хранить вечно». `ReaderReadAHead` больше не влияет на
поведение (мёртвый knob в UI) — удалить или переопределить.
**Timeline:** T+0 ✅ развёрнуто; T+30d проверить вытеснение каталогов целиком на реальной
нагрузке; T+180d пересмотреть granularity.

## KI-004: server.cleanCache() дублировал reconciler и гонялся с ним на старте (2026-10-01) — РЕШЕНО
**Status:** 🟢 Удалён 2026-10-01 (решение владельца), ещё не развёрнуто на прод
**Files:** `server/server.go` — удалены `cleanCache()`, `calculateDirStats()`, `removeAllFiles()`
и вызов `go cleanCache()`
**Root Cause (было):** Оба ходят по TorrentsSavePath при старте. `cleanCache` удалял каталоги
торрентов, которых нет в списке, одновременно с первым сканом reconciler'а. Побочный эффект,
измеренный на проде: удалён кеш «Укрытие (Silo) Сезон 3» (28 ГБ) — торрент просто не был в
списке, и это шло **мимо проверки лимита**.
**Почему reconciler покрывает эту функцию:** он измеряет каталог, считает осиротевшие данные
частью бюджета и вытесняет их coldest-first под лимит — на старте и далее каждые 5 минут.
**Следствие (семантика, согласовано с владельцем):** каталог торрента, которого нет в списке,
больше не удаляется мгновенно, а становится кандидатом на вытеснение по бюджету.
**Состояние на проде:** код готов, бинарь `29e3072a…` лежит `/opt/torrserver/TorrServer.v2`,
переключение отложено до окончания просмотра (вечер 2026-10-01).

## KI-005: ReaderReadAHead переведён из процентов кеша в абсолютные МБ (2026-10-01)
**Status:** 🟢 Код готов + тесты, не развёрнуто (вместе с KI-004)
**Files:** `settings/btsets.go` (поле `ReaderReadAheadMB`, JSON-ключ прежний
`ReaderReadAHead`, константы Min/Max/Default), `torr/torrent.go` (`readAheadBytes`,
`updateRA`), `tgbot/admin_settings.go` (кнопки 8/16/32/64 MB), `tgbot/admin_preset.go`
(performance 64, streaming 32, storage/low 16), `torr/readahead_test.go` (3 теста)
**Почему:** после фикса окна ридера настройка перестала на что-либо влиять, но оставалась в UI.
Процент от глобального кеша не имеет физического смысла; MB — имеет.
**Миграция:** JSON-ключ сохранён, старые значения 5..100 становятся 5..100 МБ (всё в диапазоне
4..1024). Значение 81 у владельца → 81 МБ.
**Guard (важно):** границы дублируются в точке применения (`readAheadBytes`), а не только в
валидации настроек. Это поймал тест `TestReadAheadBytesFollowsConfiguredMB`: без дублирования
значение 99999 превращалось в окно 93 ГБ — ровно тот баг, который чинили.
**Проверено тестами:** окно ≤ 1.1 % кеша на максимуме; clamp по доле кеша на маленьком кеше.

## KI-006: nil-указатель на `bts.client` при переподключении клиента (2026-10-04)
**Status:** исправлено в `d739331` (локальная v3), в апстриме не заявлено — баг существует и в порте
**Files:** `torr/apihelper.go:324` (`WriteStatus` → `/stat`), `torr/torrent.go:61` (`NewTorrent`),
`btserver.go:62` (`Connect`), `btserver.go:76` (`Disconnect`), `web/pages/route.go:43`
**Суть:** `Disconnect()` присваивает `bt.client = nil`, а затем `SetSettings` вызывает `Connect()`.
Читатели `bts.client` **не входили в `bt.mu`**, поэтому любой запрос, попавший в это окно (~2 с),
разыменовывал nil внутри библиотеки: `client.go:1461 cl._mu.RLock()`.
`Connect`/`Disconnect` писали под `bt.mu.Lock()` — мьютекс был, но читатели в него не участвовали,
а мьютекс синхронизирует только тех, кто его использует.
**Наблюдение на проде (2026-10-04 13:27:22):** `panic: invalid memory address or nil pointer dereference`,
стек `client.go:1461 ← client.go:169 ← apihelper.go:326 ← route.go:43`, перехвачен `gin recovery`.
За этот день — 7 паник. Триггер — любое сохранение настроек в веб-интерфейсе.
**Почему важно:** `SetSettings` дополнительно вызывает `dropAllTorrent()`, поэтому сохранение настроек
обрывает все активные загрузки; это отдельная проблема UX, не исправленная здесь.
**Второе место с тем же классом:** `torrent.go:63` проверял `bt.client == nil`, но на 17 строк выше
использования — guard, который не защищает от чересполосицы (подтверждено исследованием: go-nostr PR #183,
«проверка `if Connection != nil` не решает проблему — значение может стать nil сразу после проверки»).
**Guard (чем закрыто):** читатели берут `bt.mu.RLock` и **держат его на время вызова**, чтобы клиент
не был `Close()` под ними; nil-клиент выдаётся сообщением, а не разыменовывается. `NewTorrent`
получает ту же защиту непосредственно перед вызовом, и read-lock снимается **явно, а не через `defer`**,
так как ниже берётся `bt.mu.Lock()`, а `sync.RWMutex` не переиспользуется (иначе — дедлок на каждом
добавлении торрента; пойман Red Team, зафиксирован тестом).
**Тесты:** `torr/clientrace_test.go` — `TestWriteStatusNilClient` (воспроизводит продакшн-стек
дословно, без фикса падает), `TestConcurrentDisconnectAndWriteStatus` (50 переподключений против
4 читателей), `TestNewTorrentDoesNotDeadlock` (зависает на полном таймауте при `defer`).
Проверено в обе стороны: сломанный вариант падает/висит, рабочий проходит.
**Не проверено:** детектор гонок `-race` недоступен локально (`CGO_ENABLED=0`, нет gcc), поэтому
аспект гонки **данных** подтверждён чтением кода и детерминированным воспроизведением nil-состояния,
но не самим детектором. Гонка подтверждена на живом сервере (7 паник), лечение — кодом и тестами.

## KI-01x: Flake — TestNewTorrentDoesNotDeadlock (2026-10-10)
**Status:** 🟡 Наблюдаем
**File:** `server/torr/clientrace_test.go:128`
**Описание:** полный прогон `go test ./torr/...` дал FAIL **1 из 3**; два повторных прогона пакета —
PASS. Deadlock-проба чувствительна к таймингу под нагрузкой (`go test ./torr/...` гоняет 3 пакета
параллельно). Причина не установлена; выигрыш-условие слишком узкое.
**Deadline:** если повторится ≥2 раз в разных сессиях — T7: guard (детерминированный сигнал вместо
таймаута + выровненный ресурс).
**Примечание (порт #900):** `f516a84b` — bufio 1 MiB. bufio зовёт наш `Reader.Read` с `p≈1 MiB`,
поэтому `r.offset` (окно защиты) уходит вперёд до 1 MiB за фактически переданным клиенту байтом.
Для окна 90 МиБ и кэша 88 ГиБ потеря — 1%, принята; на стенде 1 ГиБ перепроверяется при следующем
замере. Коммиты `aaed3654` + `f516a84b` локальны, **прод-бинарь не обновлён** (3aa5ccef).

## KI-007: Бэкпорт трёх апстрим-фиксов — preload nil, stream 404/503, torznab URL+UA (2026-10-17) — РАЗВЁРНУТО
**Status:** 🟢 Развёрнуто на 192.168.2.6 2026-10-17 11:36 +03 (sha `02d3cbb3…`; бэкап
`/opt/torrserver/TorrServer.bak-20261017-113616` = прежний `bf88f13c…`; сервис `active`, `/stat` 200, `journalctl -p err` пусто)
**Files:** `torr/preload.go`, `torr/stream.go`, `torznab/torznab.go` + тесты
**1. Preload vs drop (апстрим `c3a0642e`, issue #916):** `drop()` обнуляет `t.Torrent`, оставляя
`Stat`; `Preload` проверял только `Stat` и падал на `t.Files()[0]` — `index out of range [0]`.
Фикс: снапшот `torrent := t.Torrent` под `muTorrent` + ранний выход при `t.Torrent == nil`; снапшот
используется везде (частая ошибка — только nil-проверка; между проверкой и `t.Files()` `drop()`
успевает обнулить — TOCTOU).
**2. Пустой 200 при сбое стрима (апстрим `ef4e461d`, PR #845):** `Stream` возвращал ошибку без
записи ответа → gin слал `200 OK` с пустым телом, плеер читал как файл нулевой длины. Фикс:
`http.Error` 404 (файл не найден) / 503 (reader не создан).
**3. Torznab URL + UA (апстрим `74e65fb2`, PR #919):** хост, заканчивающийся на `/api`, превращался
в `/api/api`; без User-Agent индексеры за WAF отдают 403. Фикс: `buildAPIURL` нормализует хост,
`httpGet` шлёт `User-Agent: TorrServer-Torznab/<ver>`.
**Проверка (two-commit, §19.12):** каждый фикс — тест красный на старом коде, зелёный на новом
(`preload_nil_test.go`, `stream_status_test.go`, `torznab_backport_test.go`, `torznab_url_test.go`);
torznab UA подтверждён: old = `Go-http-client/1.1`, path `/api/api`; new = `TorrServer-Torznab/MatriX.144`, path `/api`.
**Не портировано из того же `ef4e461d`:** race-фиксы GotInfo под локом, `expiredTime`, `Status`
сортирует копию `Files`, а не сам слайс торрента — отдельная задача.
**Предсуществующее (не связано):** `go test ./...` — `server/rutor` FAIL: `mem_test.go` открывает
`rutor.ls`, которого нет в репозитории (фикстура не трекается).

## KI-008: Холодный старт DHT — первый announce новой раздачи на пустой таблице ждёт 5 мин (issue #922) (2026-10-17)
**Status:** 🟢 Развёрнуто на 192.168.2.6 2026-10-17 14:07 +03 (sha `550ea84b…`, база движка v1.2.32; бэкап
`/opt/torrserver/TorrServer.bak-20261017-140710` = прежний `91d925fa…` (v1.2.29-база); сервис `active`,
`/stat` 200, `journalctl -p err` пусто, 0 panic; откат: `sudo mv TorrServer.bak-20261017-140710 TorrServer && sudo systemctl restart torrserver`)
**Source:** https://github.com/YouROK/TorrServer/issues/922 — наш комментарий (`#issuecomment-6232150358`)
исчез (API 404, нет в repo-wide); повторно не постили.
**PR:** https://github.com/tsynik/torrent/pull/11 (base `master` == тег v1.2.32, head `ManSio:fix/cold-start-dht-announce`).
**Files:** движок — форк `github.com/ManSio/torrent` (ветка `fix/cold-start-dht-announce`, тег
`v1.2.32-mansio.1`, commit `f5b56bf`): `torrent.go` (`announceToDht`, `dhtAnnouncer`,
`nextDhtAnnounceInterval`), `dht_announce_test.go`; `server/go.mod` (replace
`anacrolix/torrent => ManSio/torrent v1.2.32-mansio.1`)
**Root Cause:** `announceToDht` держал фиксированные `time.After(5 * time.Minute)` и игнорировал
`done` обхода. Обход, стартовавший по пустой таблице DHT (`go s.Bootstrap()` асинхронен), мог
вернуться без пиров — и торрент висел до следующего анонса через 5 мин. Применимо к bare-hash
раздачам без трекеров; у нас смягчено `RetrackersMode=1` + `DefaultTrackers` + `TrackersListURL`.
Апстрим `tsynik/torrent@master` НЕ исправлен (проверено 2026-10-17) — бамп движка не помогает.
**Fix (вариант 1 из issue):** `announceToDht` возвращается по `done`; пейсинг перенесён в
`dhtAnnouncer` — 15 s пока нет пиров, удвоение до регулярных 5 мин, сброс на 5 мин при появлении
пира. Сверх 5 мин цикл не выходит (hop − chatter bound).
**Проверка (§19.12 two-commit, база v1.2.32):** поведенческий тест (stub `DhtServer`, обход
мгновенно с нулём пиров): чистая v1.2.32 → `announceToDht blocked for 5m0.0002788s` FAIL (300.00s);
ветка PR → `PASS (0.00s)`. Интервалы — `TestNextDhtAnnounceInterval` (6 кейсов, вкл. healthy-контроль).
TorrServer: build linux/amd64 ok (61 202 592 Б, sha256 `550ea84b…`), `go test ./torr/... ./torznab/...` зелёные.
**Не проверено:** в живой сети (нужен публичный рой UDP наружу) — только поведенческий тест и
чтение кода. `-race` недоступен локально (нет gcc).
**Guard:** форк-тег пиннит патч; при обновлении движка сверить, что `announceToDht` не переписан
(если PR #11 будет принят — снять replace).
**Timeline:** T+0 ✅ развёрнуто (2026-10-17 14:07); T+30d — сверить статус PR #11 и ответ в #922;
T+180d — пересмотреть пин/replace по факту мерджа PR.
**Прод-проверка 2026-10-24:** бинарь `sha256 550ea84b…`, `vcs.revision=bc427f28`, go1.27.0, active
с 17.10 14:07, 0 рестартов; в бинаре есть `nextDhtAnnounceInterval`/`dhtAnnouncer` и `v1.2.32` →
KI-008 действительно в проде.

## KI-009: issue #925 — эвикция partial-куска даёт zero-read (2026-10-24) — ИСПРАВЛЕНО В КОДЕ, не задеплоено
**Status:** 🟢 Исправлено 2026-10-24 20:20 и **задеплоено 2026-10-24 20:32**:
форк-тег `v1.2.34-mansio.1` (`9bb0213`) + вызов `tp.InvalidateStorage()` в `Piece.Release`.
**Прод:** `sha256 8a8b8829…0111c`, `vcs.revision=db2309b9`, symbol `InvalidateStorage` = 2, сервис
active, `/stat` 200, 0 ошибок/0 panic после старта. Бэкап:
`/opt/torrserver/TorrServer.bak-20261024-203242-before925`.
**Source:** https://github.com/YouROK/TorrServer/issues/925
**Files:** `../_forks/torrent/piece.go:250` (`InvalidateStorage`, внешний форк, вне репо);
`server/go.mod:6` (`ManSio/torrent v1.2.32-mansio.1` — метода НЕТ);
`server/torr/storage/torrstor/piece.go:114-126` (`Release` вызывает только `SetPriority(None)` и
`UpdateCompletion()`, `InvalidateStorage` отсутствует).
**Root Cause:** при вытеснении partial-куска storage теряет байты, а клиент продолжает считать чанки
полученными (`dirtyChunks`/`haveChunk`), поэтому повторное чтение отдаёт нули. Лечение — вернуть чанки
в pending на слое библиотеки (`InvalidateStorage` = `pendAllChunkSpecs` + `cancelRequestsForPiece` +
`tickleReaders`).
**Факт (2026-10-24, `go test ./torr/storage/torrstor -run Issue925 -v`):**
`BUG REPRODUCED: evicted region reads as ZEROS`; `CHARACTERIZED: #925 present`. Тесты — PASS, потому
что фиксируют наличие бага, а не его отсутствие.
**H5 (было опровергнуто, теперь подтверждено):** 24.10 `Release` вызывает `InvalidateStorage`
перед `SetPriority(None)`; `TestIssue925ReleaseWiringWithRealTorrent` проходит.
**Доказательство фикса (fork, база `tsynik v1.2.34`):**
- ветка `ManSio/torrent fix/invalidate-storage-v1.2.34`, тег `v1.2.34-mansio.1` (`9bb0213`), запушены;
- `TestInvalidateStorageClearsDirtyChunks` → PASS (dirtyChunks очищаются, соседний кусок — негатив-контроль);
- `go build ./...` = 0; `go test ./torr/storage/torrstor -run Issue925` = PASS.
- **Не проверено (главное):** устранение zero-read на живом торренте/рое. Для этого нужно реальное
  вытеснение partial-куска: либо заполнить кэш до лимита (~40 GiB сверх текущих 104 GiB), либо поднять
  отдельный инстанс с малым `CacheSize` и локальным сидом. Статус: **deployed, live behaviour not
  proven** — не выдавать за закрытие issue.
**Откат:** `sudo mv /opt/torrserver/TorrServer.bak-20261024-203242-before925 /opt/torrserver/TorrServer
&& sudo systemctl restart torrserver`.
**Guard:** репро-тест с положительным и отрицательным контролем остаётся в дереве; критерий закрытия —
`BUG NOT REPRODUCED` после тега `v1.2.32-mansio.2` + вызова в `Release`.
**Timeline:** T+0 — запись создана по факту проверки; T+30d — вынести фикс в тег и `go.mod`;
T+180d — снять replace после мерджа PR #11.
**Прод-проверка 2026-10-24 (read-only, 192.168.2.6):** бинарь `sha256 550ea84b…`, `vcs.revision=bc427f28`,
`vcs.modified=true`, go1.27.0; `grep -a -c InvalidateStorage` = **0** → #925 отсутствует и в проде.
Сервис active с 2026-10-17 14:07, 0 рестартов, `/stat` 200, ошибок за 48 ч нет.

## KI-010: синхронизация с upstream после 2026-10-17 (2026-10-24)
**Status:** 🟢 4 критических фикса портированы; 2 пункта отложены.
**Source inventory:** `upstream/master = 4bfe69e8`; отчёт `experiments/upstream-sync-2026-10-24/REPORT.md`.
**Портировано:** `e806313a` WaitInfo/nil-drop (#929), `6ccaebbf` литеральный `%` в query (#928),
`4bfe69e8` рестарт DLNA/Bonjour/rutor после `def` (#934), `6fba8284` таймауты DNS (#892).
**N/A:** `7a101b8f` #914 — в нашем форке нет `statusFromSpec` (all.m3u строит вложенные плейлисты).
**Отклонено владельцем:** `d592c561` #911 внешние субтитры (`/subs`) — не внедряем.
**Вторая волна (2026-10-24):**
- `bd07eb40` torrent v1.2.34 — **внедрено**: `replace` переведён с `ManSio/torrent v1.2.32-mansio.1`
  на `tsynik/torrent v1.2.34`. Проверено по коду модуля: v1.2.34 содержит тот же DHT-фикс
  (`nextDhtAnnounceInterval`, retry 15 s, выход по `done`), поэтому форк больше не нужен.
- `4e163a78` #678 migrate — **внедрено**: существующий ключ в target больше не перезаписывается;
  `migrate_target_wins_test.go` (regression + positive control).
- `76cd0c17` #910 docker args — **внедрено** (`--tgtoken`, `--fusepath`).
- `4aa1061f` #908 web revalidate — **частично**: `/` отдаётся с `no-cache`, сильный quoted ETag и
  304 по `If-None-Match`; полная регенерация static-заголовков/`gen_web.go` отложена.
- `5e9ed1f2` #923 — не нужен без #911.
**Решение по движку:** `server/go.mod` теперь `github.com/anacrolix/torrent => github.com/ManSio/torrent v1.2.34-mansio.1`.
База — `tsynik/torrent v1.2.34`, который сам является мерджем нашего PR #11 (DHT-фикс), поэтому форк
теперь несёт **только** `InvalidateStorage` (#925). `v1.2.32-mansio.1` больше не используется.
**Guard:** `TestUnescapeQuery`; `restartServicesMu` (сериализация конкурентных `/settings`);
build/vet/tests — зелёные. `-race` недоступен (нет cgo).
**Red Team:** `experiments/upstream-sync-2026-10-24/RED_TEAM.md` — 2 атаки закрыты, 2 приняты как
upstream-identical (двойной decode `%25`, `TorrentSpec` без лока).
## KI-011: флейк — TestReconcileFindsPreExistingOrphans (2026-10-24) — ИСПРАВЛЕНО 2026-11-06
**Status:** 🟢 Fixed. Причина: `NewStorage` сразу запускает `cleanupLoop`, который делает
`reconcileOrphans(true)` и мог посчитать фикстуру между её созданием и проверкой `== 0`.
Guard: `Storage.WaitInitialScan()`; `newIdleStorage` ждёт первый скан. Проверка:
`-count=20` — 20/20 PASS.
**File:** `server/torr/storage/torrstor/cache_limit_test.go:246`
**Симптом:** `setup wrong: server already counts 8388608 bytes` сразу после создания 16 файлов
фикстуры, до `reconcileOrphans(true)`.
**Гипотеза:** фоновый reconciler/cleanupLoop успевает просканировать `TorrentsSavePath` между
созданием файлов и проверкой; тест опирается на «счётчик == 0» в окне, где это не гарантировано.
**Проверка:** повторные `go test ./torr/storage/torrstor -count=1` — 2/2 PASS, т.е. флейк, не
детерминированная регрессия; движок/наши правки в этом тесте не участвуют.
**Deadline:** при повторе ≥2 раз — детерминированный guard (остановить worker на время фикстуры
или вынести фикстуру в отдельный путь без фонового скана).

## KI-012: дыры, закрытые из закрытых PR/engine v1.2.36 (2026-10-24)
**Status:** 🟢 код готов и протестирован; деплой — в этом же цикле.
**Взято из `lampac-nextgen/torrent` v1.2.36 (не из #939 целиком):**
- `1013ca2` + `lampac-nextgen/utp v0.2.1`: `Client.Close` больше не оставляет DHT serve
  заблокированным в uTP `ReadFrom` (у нас в v1.2.34 стоял `// TODO`). Актуально: сервер
  пересоздаёт клиент на каждое сохранение настроек.
- `8b29b49`: кусок под читателем качается с нескольких пиров (кап `maxStallProneRequests=3`),
  остальная зона — по-прежнему с duplicate-timer. Заявка автора: 1.82→4.22 MiB/s, стойки 79→47.
**Взято из #909/`ef4e461d` (merged upstream):** `GotInfo` не дёргает `Stat` без лока,
`expiredTime` под `muTorrent`, `Close` check-and-set под локом, `Status` сортирует копию `Files`.
**Взято из #938 (open):** ETag `.m3u` считается от тела, а не от `hash/name`.
**Свои дыры:** ffprobe-контекст больше не оставляет goroutine на 5 минут; DNS-cache не удаляет
под `RLock` и отдаёт копию; `TDB.CloseDB` сбрасывает `globalBboltDB`.
**Не взято:** `a0ceb3b` drop-chunks-no-reader (конфликт с background-fetch/pause-window),
`4371696` reader tiers (процентная семантика — регресс KI-003/005).
**Проверка:** build 0; `go test ./torr/... ./torrstor/... ./cmd/... ./web/api/... ./settings/...` — ok.
**Деплой 2026-10-24 21:06:** бинарь `sha256 ce047db0…d6ad`, `vcs.revision=1c497f64`;
бэкап `/opt/torrserver/TorrServer.bak-20261024-210640-beforeholes`. Сервис active, `/stat` 200,
0 ошибок/panic. Символы в бинаре: `InvalidateStorage`=2, `inFlightRequests`=3, `lampac-nextgen/utp`=3.
**Живая проверка reconnect:** `POST /settings {"action":"set"}` с текущими настройками — `200`
за **3.78 с**, сервис active, `/stat` 200, 0 ошибок/panic после (это путь `Disconnect → Client.Close`,
т.е. DHT-close отработал).
**Не проверено:** A/B multi-peer на реальном торренте (нужен рой и замер скорости/стоек) и
устранение zero-read #925 (нужно вытеснение куска).

## KI-013: догоняем upstream MatriX.148–150 (2026-11-06)
**Status:** 🟢 критическое портировано в этом цикле; остальное осознанно отложено.
**Портировано:**
- `6fd3bd82` #957 — `//go:debug multipathtcp=0` в `cmd/main.go` (снижение RCE-риска CVE-2026-98432).
- `8a69eadd` #941 — `hashFromHex`: битый hash в API даёт not found, а не панику.
- `af1a4917` #942 — `{"action":"set"}` без `sets` → 400, а не nil-паника.
- `98c5af2b` #955 — эвикция в RAM-режиме считает выделенный буфер (`memCost`); у нас
  глобальный бюджет уже считал аллокацию, добавлена согласованность per-cache.
- `f6022df7` #952 — Torznab-хост без `http://` нормализуется в `buildAPIURL`.
- `KI-011` — флейк reconcile закрыт `WaitInitialScan()`.
**Отложено (не критично / большая фича):**
- `9813a9c0` #933 torrfs одинаковые имена; `00e55966` #954 gstreamer task на сессию (sid);
  `33950464` #947 единицы размера Torznab; WAF #943/#944/#946; #924 ReadFile (у нас нет `/subs`);
  web #951/#956/#959/#960; gstreamer #958.
**Проверка:** build 0; `go test ./torr/... ./torrstor/... ./web/api/... ./torznab/... ./cmd/...` ok;
`TestReconcileFindsPreExistingOrphans -count=20` 20/20.
**Деплой 2026-11-06 23:31:** `sha256 6855dd53…da07`, `vcs.revision` = коммит a54ad72e; бэкап
`/opt/torrserver/TorrServer.bak-20261106-233106-before148`. Живые проверки: `/stat` 200,
битый hash → **404**, `{"action":"set"}` без `sets` → **400**, `multipathtcp` в бинаре,
0 ошибок/panic. (Первый `curl` сразу после старта дал 000 — сервер ещё не слушал; повторный 200.)
