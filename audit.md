# Scrinium codebase audit — заметки для следующей сессии

После M1.4 финализации. ~10K LOC prod / ~7.2K LOC tests / 310 тестов / 18 stub-методов.

Проблемы расставлены по приоритету: P0 = безусловно чинить, P1 = стоит чинить, P2 = косметика, P3 = заметки на будущее.

---

## P0 — реальные дефекты, чинить безусловно

### P0-1. Mass type duplication между `domain/` и `core/`

**Где:** `domain/state.go` (82 LOC) + `domain/options.go` (частично).

**Что:** следующие типы определены **дважды** — в `domain/` и в `core/`. Версия из `domain/` нигде в кодовой базе не используется (`grep domain.PhysicalAddress` — пусто, и т.д. для каждого).

| Тип | В `domain/` | В `core/` | Используется |
|---|---|---|---|
| `Workspace`, `WorkspaceLocation`, `WorkspaceHost` | state.go | state.go | core версия |
| `PhysicalAddress` | state.go | state.go | core версия |
| `StorageInfo` | state.go | state.go | core версия |
| `BlobExistStatus`, `BlobNotFound/Exists/IsTombstone` | state.go | index.go | core версия |
| `PackedBlobInfo` | state.go | index.go | core версия |
| `PackedEntry` | state.go | index.go | core версия |
| `StoreState` + 5 констант | state.go | state.go | core версия |
| `MaintenanceMode` + 3 константы | state.go | state.go | core версия |
| `PutOptions` | options.go | options.go | core версия |
| `GetOptions` | options.go | options.go | core версия |
| `RoutingHints` | options.go | options.go | core версия |

**Что используется из `domain/options.go`:** только `BlobType` + 3 константы (через `internal/blobpath`).

**Решение:**
- Удалить `domain/state.go` целиком.
- В `domain/options.go` оставить только `BlobType` + константы; убрать `PutOptions`, `GetOptions`, `RoutingHints`.
- Перепроверить грепом, что ни один тест не импортирует removed types.

**Стоит:** удаление ~120 LOC dead code. Возможно ломает 0 тестов (типы реально не использовались).

**Риск:** низкий — Go compiler сразу скажет если что-то пропущено.

---

### P0-2. Stale M1.3 references в комментариях

**Где:**
- `core/store_impl.go:50,73,207,226` — упоминания «M1.3» в doc-комментах.
- `core/lifecycle.go:134,148,187,291,306` — то же.

**Что:** проект на M1.4, перешли на system.config модель, descriptor §10.1.3, и т.д. Комментарии типа «M1.3 honours the namespace-syntax rules but does not yet block calls based on token contents» — устарели либо требуют обновления до M1.4.

**Решение:** sweep по всем `M1.3`, обновить до текущего milestone. ~10 мест.

---

## P1 — стоит чинить

### P1-1. 35 неиспользуемых sentinels в `errs/`

**Где:** `errs/*.go` — 70 определений, 35 нигде не reference'нуты (50%).

**Категории:**

(a) **Intentional reservation под M2-M5** (оставить, отметить):
- `ErrAgentAlreadyRunning`, `ErrAgentNotRunning` — agent (M3)
- `ErrCuratorClosed`, `ErrStoreNotRegistered`, `ErrDrainNoTarget` — curator (M4)
- `ErrDecryptionFailed`, `ErrInvalidKDFParams`, `ErrInvalidKey`, `ErrInvalidRecoveryKey`, `ErrKeyNotFound`, `ErrPassphraseProvider`, `ErrPassphraseRequired`, `ErrRecoveryKitCorrupted`, `ErrRecoveryKitRequired` — crypto (M2)
- `ErrEjectorQueueFull`, `ErrHostStorageFull`, `ErrHostStorageLocked`, `ErrHostStorageRequired` — HostStorage (M4)
- `ErrLeaseLost`, `ErrSharedIndexRequired` — multi-host (M3+)
- `ErrFUSENotSupported`, `ErrWebDAVNotSupported`, `ErrViewClosed` — projection (M6)
- `ErrNoSnapshot` — snapshot agent (M3)
- `ErrUnknownPackFormat`, `ErrManifestsLost` — pack/recovery (M3+)
- `ErrMaintenanceInProgress`, `ErrArchivedArtifact` — misc M3+

(b) **Сомнительные дубликаты** (рассмотреть схлопывание):
- `ErrCorruptedContent` — есть `ErrCorruptedBlob`, `ErrCorruptedManifest`, `ErrIndexCorrupted`. Что покрывает Content? Возможно мёртвый.
- `ErrIsADirectory`, `ErrNotADirectory` — driver-уровень. Если localfs их не использует — мёртвые.
- `ErrInvalidPath`, `ErrPathNotFound` — то же.
- `ErrManifestTooLarge` — лимит в коде есть (`64*1024 для Metadata`), но именно ManifestTooLarge не возвращается.
- `ErrRandomAccessNotSupported` — driver capability check, но не уверен что используется в коде.

**Решение:**
- Подкатегорию (a) оставить, добавить `// reserved for MX` в doc-комменте каждого, чтобы было ясно.
- Подкатегорию (b) — проверить каждое: либо найти где должно использоваться и подключить, либо удалить.

---

### P1-2. Unification of common logic in InitStore/OpenStore

**Где:** `core/lifecycle.go:151-287` (InitStore) и `core/lifecycle.go:317-432` (OpenStore).

**Что:** Общая логика ~30-40 строк дублирована:
- Resolve options.
- Validate StoreIndex required.
- Construct `*store` struct.
- Run `recoverOrphans` + `publishOrphanReport`.
- Transition `state := StateUnlocked`.

**Решение:** выделить helper `buildStore(o, drv, idx, cfg, storeID) (*store, error)` который делает construction + orphan scan + state transition. InitStore и OpenStore сводятся к разным prologue (write descriptor + sysconfig vs read descriptor + sysconfig) + общий tail.

**Эффект:** сокращение ~30 LOC, одна точка для будущих изменений (например когда M2 добавит `Locked → Bootstrapping → Unlocked` цикл, обе функции переключатся синхронно).

**Риск:** низкий, чистый рефакторинг.

---

### P1-3. `internal/manifestcodec/codec.go` — 450 LOC одним файлом

**Что:** содержит `EncodeFile`, `DecodeFile`, `ComputeArtifactID`, `VerifyArtifactID`, `HashStream`, `marshalBodyJSON`, `unmarshalBodyJSON`, `pipeline*`, `format/parseRFC3339`. Все важные, логически связные, но большой файл.

**Решение:** разделить на:
- `header.go` — magic bytes, crypto flags, header parsing
- `encode.go` — `EncodeFile`, `marshalBodyJSON`, `pipelineToJSON`
- `decode.go` — `DecodeFile`, `unmarshalBodyJSON`, `pipelineFromJSON`
- `artifactid.go` — `ComputeArtifactID`, `VerifyArtifactID`, `HashStream`
- `time.go` — `formatRFC3339`, `parseRFC3339`

**Эффект:** каждый файл < 150 LOC, легче навигировать.

**Риск:** низкий, но требует прогона тестов после.

**Альтернатива:** оставить как есть. 450 LOC не критично, и текущая структура с ясными секциями (`// --- Body JSON encoding ---`) читается. Решить когда понадобится M2 binary encoder — тогда естественно разделить.

---

## P2 — косметика / заметки

### P2-1. Capability token поле в `*store` неактивно

**Где:** `core/store_impl.go:53` поле `capabilityToken []byte`.

**Что:** комментарий говорит «M1.3 treats the token as opt-in metadata: presence does not yet restrict, absence does not yet block». То есть поле есть, но enforcement отложен на M2+.

**Решение:** оставить как есть, обновить комментарий до M1.4. Поле — заготовка под M2 RBAC.

---

### P2-2. `plugin/` — пустой пакет с placeholder constant

**Где:** `plugin/doc.go` — единственный файл, `const placeholder = "scrinium-plugin"`.

**Что:** «entry point in the DAG; concrete subpackages... appear in M2.1». Это заглушка для топологии пакетов.

**Решение:** оставить. При появлении первого реального plugin'a (например `plugin/hash/blake3`) заглушка естественно удалится.

---

### P2-3. Verify и Get — общая логика чтения блоба

**Где:** `core/get.go:118-127` (Target branch) и `core/verify.go:86-104` (та же логика).

**Что:** оба используют `index.Resolve(BlobRef) → addr → drv.Get(addr.Path) → процесс`. Verify хеширует, Get отдаёт reader.

**Решение (опционально):** выделить helper `s.openBlob(ctx, blobRef) (io.ReadCloser, error)`. Но различия в обработке ошибок (Verify свопает `os.ErrNotExist` на `ErrCorruptedBlob`, Get — нет) могут затушить семантику. Сейчас не критично.

---

### P2-4. `event` constants раскиданы по пакетам

**Где:** `core/events.go`, `agent/events.go`, `index/events.go`, `curator/curator.go` (events частью), `projection/projection.go` (events частью).

**Что:** каждый пакет декларирует свои события. Соглашение о префиксах есть (см. `agent/agent.go` doc): `core.*`, `agent.*`, `curator.*`, `index.*`, `projection.*`.

**Решение:** **оставить как есть.** Per-package events — правильная архитектура: новый пакет приносит свои события, не надо центрального registry. Доку про префиксы (в `agent/agent.go`) можно вынести в общую `event/doc.go` — это полезно.

---

### P2-5. Comment-density 34% — high but mostly architectural

**Что:** 34% line ratio of comments to code. Высоко, но при чтении видно что это не «// add 1 to x» а архитектурные обоснования + cross-spec ссылки.

**Решение:** не трогать. Это полезная самодокументация.

---

## P3 — to consider, but not now

### P3-1. ShardDepth/ShardWidth/AlgoPlacement параметризация (OQ-21)

Зафиксировано в backlog как открытый вопрос. Инвариант *manifest identity-only* уже зафиксирован в коде (read-path через `index.Resolve`). Параметризацию вводить при первом реальном запросе.

### P3-2. Reshuffle Agent (OQ-21)

Дёшевая операция (`os.Rename` в пределах FS), но требует прерываемости и idempotency. ~500-800 LOC + тесты. Откладывается до момента когда понадобится.

### P3-3. CountManifests в StoreIndex для cheap Capacity

Сейчас Capacity делает linear scan через `ListByNamespace("*")`. Для М1.4 ОК, при росте до 10M+ артефактов нужен cheap COUNT(*) метод в интерфейсе StoreIndex.

### P3-4. Smoke tests Ramdisk вариант

Уже есть в Makefile комментарий с macOS/Linux one-liners. Можно автоматизировать через отдельный target `make smoke-ramdisk`, но ручное использование тоже работает.

---

## Что НЕ проблема (намеренная избыточность)

Эти места могут выглядеть «лишними», но они являются заготовкой под спекульто запланированные расширения. Не трогать.

1. **`curator/` пакет ~764 LOC, `curator.New() = "not implemented"`.** — Public surface зафиксирован для M4. Все типы (`WriteStrategy`, `ReadCost`, `ReadPolicy`, `BackupConfig`, и т.д.) — контракт, не код. Реализация лендится в M4.

2. **`agent/` пакет ~518 LOC, все агенты — `AgentNotImplementedYet`-style stubs.** — Public SPI готов для M3. `BackgroundAgent` интерфейс + payload-структуры `AgentStartedPayload`/`AgentProgressPayload`/`AgentFailedPayload`. Concrete agents (Ingester, GC, Scrub, Ejector) — M3.

3. **`maintenance/rebuild.go` — stub.** — `RebuildIndexAgent` под M3 recovery.

4. **`projection/projection.go` — public surface готов, `NewProjection() = "not implemented"`.** — M6.1 territory.

5. **18 stub-методов в `core/store_impl.go`, `agent/*.go`, `curator/*.go`** — все обоснованы deferred milestones (M2/M3/M4/M5). См. doc-комменты каждого.

6. **`event` constants в каждом пакете**, не центральный registry — правильно. Per-package ownership.

7. **`StatePaused` в `agent.AgentState` enum** — заготовка под «auto-pause under pressure» backlog item. Intentional.

8. **Capability token в `store` struct** — M2 RBAC заготовка.

9. **`ManifestEncodingBinary`, `ManifestCryptoEnvelope`, `ManifestCryptoMetadataOnly`** в domain — enum-значения уже определены, реализация в manifestcodec возвращает `ErrUnsupportedEncoding`/`ErrUnsupportedCrypto` для M1.4. Перейдут в живой код в M2/M2.1.

10. **`internal/blobpath.RefFromPath` поддерживает Sharded и Flat parsing** — для recovery scan с разными topology. Defensive, не лишнее.

---

## Снапшот цифр на момент M1.4

```
Production:    9989 LOC, 84 files
Tests:         7228 LOC, 310 tests
Test ratio:    0.72
Comment density: 34%
Stubs:         18 (все intentional, deferred milestones)
Sentinels:     70 defined / 35 active / 35 reserved-or-dead

Largest files:
  index/sqlite/manifest.go  555 LOC  (transactional index core)
  core/lifecycle.go         490 LOC  (InitStore + OpenStore)
  internal/manifestcodec/codec.go  450 LOC (binary + JSON codec)
  core/put.go               372 LOC  (Put pipeline)
  core/store_impl.go        318 LOC  (Store impl + stubs)
  core/get.go               295 LOC  (Get pipeline)
```

---

## План на следующую сессию (если приоритет — чистка)

1. **P0-1** — удалить мёртвые типы из `domain/state.go` и `domain/options.go`. ~30 минут с прогоном тестов.
2. **P0-2** — sweep stale M1.3 → M1.4 references. ~15 минут.
3. **P1-1(b)** — проверить сомнительные sentinels. ~30 минут.
4. **P1-1(a)** — добавить `// reserved for MX` маркеры на intentional reservation. ~15 минут.

Total: ~1.5 часа на P0 + быстрые P1. Это закрывает всю «накипь» после M1.4 и оставляет код в чистом состоянии перед началом M2.

P1-2 (унификация InitStore/OpenStore) и P1-3 (split manifestcodec) — отдельная сессия, если решишь делать.

P2 и P3 — заметки на будущее, не трогать сейчас.