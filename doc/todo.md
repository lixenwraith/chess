# Replay Implementation Tasks

This plan covers the remaining work needed for first-class replay in the web
and CLI clients, durable player-game browsing, and a curated archive of famous
games. The persistence/API foundation completed by the database audit is listed
first so later work does not duplicate or bypass it.

## Foundation Available Now

- [x] Store terminal `result` and `end_time_utc` on each game.
- [x] Persist `white_claimed_by` and `black_claimed_by`, including claims made
  on the first valid move after game creation.
- [x] Commit each move, first-move claim, and move-caused terminal result in one
  SQLite transaction.
- [x] Rewind moves and clear a stale terminal result/end time in one transaction.
- [x] Return ordered UCI moves with `fenAfterMove` through
  `GET /api/v1/games/{gameId}/history`.
- [x] Return bounded pages through authenticated
  `GET /api/v1/users/me/games?limit=&offset=`.
- [x] Provide matching Go client DTOs and methods (`GetGameHistory`,
  `GetMyGames`) without prematurely adding CLI presentation.
- [x] Evict terminal games from memory after a configurable TTL while retaining
  durable rows and moves.
- [x] Add an async-write barrier and a single SQLite read snapshot for immediate,
  internally consistent replay reads.
- [x] Configure browser API origin through `/config`, with `/chess` fallback for
  the static deployment at `lixen.com/projects/chess/`.
- [x] Add debug-level persistence, cleanup, engine queue, and lifecycle logging.

## Decisions Required Before Replay UI Work

| Decision | Current behavior | Decision needed |
|---|---|---|
| History visibility | Public to anyone with a game UUID, like live game reads | Keep public, make games private by default, or add per-game visibility |
| Live mutation authorization | Configure, undo, computer-trigger, and unload remain UUID-based; claimed slots protect human moves only | Define owner/opponent/spectator permissions before replay and archive UI expose more game discovery |
| Database retention | Indefinite; only terminal in-memory state is evicted | Retention by account type, archive status, age, or explicit deletion |
| Delete semantics | `DELETE /games/{id}` unloads memory but retains history | Name it “close/unload,” or add a separate authorized durable delete |
| Durability guarantee | Gameplay continues after a write failure; health becomes degraded | Keep best-effort, acknowledge writes, retry with an outbox, or fail gameplay closed |
| Result model | `white_wins`, `black_wins`, `draw`, `stalemate` | Keep compatibility or split outcome (`1-0`, `0-1`, `1/2-1/2`) from termination reason |
| Archived-game owner | Not created | Protected system/demo user, separate archive owner table, or ownerless source records |
| Replay notation | UCI plus authoritative FEN after each move | Add SAN and canonical PGN at import/write time or derive them on read |

Record these choices in an ADR before changing the v1 response contract. Until
privacy is decided, do not add searchable public player-game indexes or expose
usernames in public history.

## Phase 1 — Complete the Durable Game Model

### Results and termination

- [ ] Represent outcome separately from termination reason. Candidate fields:
  `outcome`, `termination`, and optional `result_detail`.
- [ ] Detect and persist all supported draw paths, not only stalemate:
  insufficient material, repetition, fifty/seventy-five-move rule, and agreed
  draw if that interaction is added.
- [ ] Define behavior for resignation, timeout, abandonment, engine failure,
  and administrative termination.
- [ ] Add constraints covering valid combinations: an end time requires a
  terminal outcome; an ongoing game has neither.
- [ ] Decide whether undoing a finished rated/player game is allowed. If yes,
  preserve an audit event rather than silently rewriting official history.

### Stable participant metadata

- [ ] Snapshot display names at game start/end so replay remains readable after
  a user rename or temporary-account deletion.
- [ ] Separate historical participant identity from mutable controller config.
  Changing a human slot to a computer must never remove the user's game link.
- [ ] Decide whether anonymous players receive a durable pseudonym, remain
  unnamed, or are excluded from archive browsing.
- [ ] Add optional clocks/time-control metadata before timeout results are
  supported.

### Notation and integrity

- [ ] Add SAN per ply and canonical PGN, or add a deterministic backend
  converter from the stored initial FEN/UCI line.
- [ ] Validate that `move_number`, `player_color`, FEN side-to-move, and the
  previous position form one legal continuous line.
- [ ] Add a stored content hash for import idempotency and corruption checks.
- [ ] Add a repair/audit CLI command that reports broken game rows without
  mutating them; make repair an explicit separate operation.
- [ ] Define a schema-migration policy beyond v2, including forward-version
  rejection, backup instructions, and rollback limitations.

## Phase 2 — Replay and Library APIs

### Player games

- [ ] Add filters to the authenticated list: `status`, `result`, color, opponent
  type, and date range.
- [ ] Replace offset pagination with a stable `(start_time_utc, game_id)` cursor
  before the table grows large; retain v1 offset parameters during migration.
- [ ] Return a compact display label/opponent summary so clients do not recreate
  association logic.
- [ ] Define whether an authenticated user may list a game merely created for
  their random player ID versus one explicitly claimed by them.
- [ ] Add authorization tests for expired/deleted sessions and attempts to list
  another user's games.

### Replay payload

- [ ] Version the history payload before adding annotations, evaluations,
  comments, variations, clocks, or PGN tags.
- [ ] Include a canonical final FEN and normalized outcome/termination fields.
- [ ] Decide whether long games return one payload or paged/chunked moves.
- [ ] Add `ETag`/`If-None-Match` for immutable finished histories.
- [ ] Add a downloadable PGN response with correct `Content-Type` and filename.
- [ ] Return an explicit “ongoing/incomplete” marker when history is requested
  before a terminal result.

### Live-game restoration

- [ ] Decide whether a server restart should make unfinished games playable or
  replay-only.
- [ ] If play must resume, load the last persisted FEN, next turn, player config,
  claims, and move list into memory at startup.
- [ ] Mark games interrupted in `pending` state as recoverable `stuck` or
  `ongoing`; never re-submit an engine task blindly.
- [ ] Define reconciliation when the service previously entered degraded mode
  and memory contains moves absent from SQLite.

## Phase 3 — Curated Famous-Game Archive

### Schema and ownership

- [ ] Add a game origin such as `player`, `curated`, or `imported`.
- [ ] Add searchable archive metadata: title, event, site, event date, round,
  white/black display names, Elo values, ECO/opening, source URL, source license,
  attribution text, and import timestamp.
- [ ] Add publication state, featured flag, and explicit featured rank/order.
- [ ] Create a protected demo/system identity only if ownership remains tied to
  users. It must not consume temporary-user capacity, expire, authenticate, or
  be evicted/deleted through normal user tools.
- [ ] Prefer a separate protected archive owner over credentials embedded in
  seed scripts.
- [ ] Add only indexes backed by actual archive queries; confirm each with
  `EXPLAIN QUERY PLAN` and a representative data volume.

### Import pipeline

- [ ] Add `chess-server db archive import` for one PGN or a directory.
- [ ] Parse PGN tags, comments, NAGs, and variations deliberately; document
  which are preserved and which are discarded in the first version.
- [ ] Validate every main-line move from its initial position and generate the
  authoritative FEN sequence before opening the transaction.
- [ ] Import a game and all moves in one transaction.
- [ ] Make repeated imports idempotent by source key/content hash.
- [ ] Add dry-run, structured error output, per-file summary, and all-or-nothing
  versus continue-on-error modes.
- [ ] Preserve source attribution and verify redistribution rights for every
  bundled collection.
- [ ] Seed a small, reviewed fixture set in tests; keep large archives outside
  the executable and repository unless licensing and binary size are accepted.

### Archive API

- [ ] Add a public, bounded curated list endpoint with stable sorting.
- [ ] Add exact filters required by the UI (featured, player name, event, year,
  ECO); do not expose an unconstrained database query API.
- [ ] Reuse the same history representation for player and curated games.
- [ ] Cache immutable curated list/history responses and invalidate only on
  archive administration.

## Phase 4 — CLI Replay Experience

- [ ] Add `games`/`games mine` to call `GetMyGames`, show pagination, result,
  colors, opponent/controller, date, and move count.
- [ ] Add `games featured` after the curated endpoint exists.
- [ ] Add `replay <gameId>` and allow selection from a prior list result.
- [ ] Render the initial FEN before ply 1; never assume the standard start.
- [ ] Add next/previous/start/end navigation, move-number jump, and optional
  autoplay speed.
- [ ] Display UCI initially and SAN once the backend contract supplies it.
- [ ] Clearly separate replay state from live session state: replay commands
  must not poll, move, undo, configure, or delete the live game.
- [ ] Add `pgn save <path>` after the PGN endpoint is defined.
- [ ] Cover empty lists, ongoing histories, custom FEN, malformed/incomplete
  history, expired auth, server restart, and deleted live-memory state.

## Phase 5 — Web Replay Experience

- [ ] Add “My games” for authenticated users and a separate “Classic games”
  collection available without login.
- [ ] Build accessible loading, empty, pagination, and error states.
- [ ] Add a replay route/deep link, for example `?replay=<gameId>`, that works
  beneath `/projects/chess/` and does not assume the API shares that path.
- [ ] Initialize from `initialFen`; step by assigning the stored
  `fenAfterMove`, not by replaying moves through a browser chess engine.
- [ ] Add previous/next/start/end buttons, move-list selection, keyboard
  controls, autoplay speed, pause, and current-ply announcement.
- [ ] Disable move, computer-trigger, undo, and player-configuration actions in
  replay mode.
- [ ] Stop live long-polling when replay mode begins and restore it only when a
  live game is explicitly reopened.
- [ ] Show result, termination, players, date/event, source attribution, and
  custom-start notice.
- [ ] Make browser back/forward restore list filters and replay ply.
- [ ] Test both embedded `/config` and the deployed `/chess` fallback, including
  CORS and reverse-proxy headers.
- [ ] Add responsive and accessibility checks for board orientation, focus,
  screen-reader labels, reduced motion, and high contrast.

## Phase 6 — Durability, Operations, and Scale

- [ ] Choose and implement the durability contract from the decision table.
  For acknowledged persistence, return success only after a writer receipt or
  use a durable outbox with retries and ordering.
- [ ] Expose counters/metrics for queue depth, enqueue rejection, write latency,
  failed transaction, barrier latency, replay read latency, and terminal-memory
  eviction.
- [ ] Add request/game correlation fields to logs without logging JWTs,
  passwords, or full private payloads.
- [ ] Configure a stable production JWT signing key (prefer a secret file or
  deployment secret) so persisted sessions can survive a server restart;
  document rotation and invalidation procedures.
- [ ] Add a bounded degraded-mode recovery procedure; current behavior requires
  operator intervention/restart and cannot reconstruct missing writes.
- [ ] Benchmark list and history queries with realistic user/archive sizes and
  verify query plans in CI.
- [ ] Set WAL checkpoint and database backup procedures; test online backup and
  restore with active reads/writes.
- [ ] Define database retention separately for anonymous, temporary-user,
  permanent-user, and curated games.
- [ ] Add authorized durable deletion/anonymization if required by the privacy
  policy, with archive records protected from accidental cascades.

## Required Test Matrix

- [ ] Upgrade a production-shaped legacy database to every new schema version
  and reopen it with foreign keys enabled on multiple pooled connections.
- [ ] Read history immediately after create, move, terminal move, slot claim,
  player reconfiguration, and undo—without sleeps.
- [ ] Run concurrent legal moves from one position; exactly one may commit and
  the loser must receive `GAME_CONFLICT`.
- [ ] Submit duplicate computer triggers; only one engine task may run.
- [ ] Fill the write queue/fault SQLite and assert degraded health, visible
  logging, and documented client behavior.
- [ ] Shut down with queued writes and prove all accepted writes drain.
- [ ] Restart after a finished game and replay the exact FEN sequence/result.
- [ ] Evict a terminal game from memory and replay it from SQLite.
- [ ] Change a claimed human slot to computer and verify “My games” association
  remains.
- [ ] Verify registration duplicate/session failures roll back account creation
  and capacity eviction.
- [ ] Exercise public/private history rules for anonymous, owner, opponent, and
  unrelated authenticated clients.
- [ ] Validate imported PGNs with promotions, castling, en passant, custom FEN,
  comments, and every supported result.
- [ ] Run Go unit/race tests, HTTP integration scripts, JavaScript syntax/tests,
  and browser end-to-end replay navigation in CI.

## Replay Definition of Done

- A finished player game survives restart, appears once in its owner's list,
  and replays deterministically from the stored initial FEN to the stored final
  FEN in both clients.
- A curated game is imported idempotently with source attribution, appears in a
  stable public collection, and uses the same replay path as a player game.
- Undo, player reconfiguration, terminal eviction, and concurrent requests
  cannot produce a stale result, missing claim, duplicate ply, or mixed history
  snapshot.
- Privacy, retention, durable deletion, and degraded-write behavior are
  documented and enforced consistently by API, storage, web, and CLI layers.
- Query plans and benchmarks show no redundant indexes or unbounded list scans
  at the agreed deployment size.
