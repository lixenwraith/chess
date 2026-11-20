# Roadmap

What is left to build, in batches of numbered items: R5 on, R1–R4 being
done. Each item names its priority, the files it touches and the docs that
explain the area. A finished item leaves this file, and the line of its
batch under Done says what it delivered.

- P0: a defect in production now; the next PR.
- P1: small, and it unblocks other items or turns on shipped work.
- P2: planned next; its prerequisites are met or named.
- P3: not now: it needs a decision or other items first.

## Order of Work

- Phase 1, now:
  - R7.1 P1: verify production data against the rules core (operations)
  - R8.1 P1: deliver mail through the mail jail (operations)
- Phase 2, next, in this order:
  - R9.1 P2: interrupt long-running CLI commands
  - R6.2 P2: resume unfinished games after a restart
  - R7.2 P2: validate human moves with the core, after R7.1
  - R8.2 P2: email verification, after R8.1
  - R6.3 P2: recover from degraded storage, after R6.2
- Phase 3, later, each after its decision:
  - R9.3 P3: human against human in the web client (D8), after R6.2
  - R10.1 P3: metrics
  - R9.2 P3: analysis on a replay
  - R8.4 P3: recovery timing through the sender
  - R5.1–R5.4 P3: the curated archive, rights first

## Done

- R1 Notation core: `internal/server/chess` (strict FEN, legal moves, SAN,
  PGN, draw rules) and `db verify`; perft, a Stockfish cross-check and fuzz
  targets. Its last test gate is R7.1.
- R2 Replay API: history with SAN, PGN export (`?ply=`), keyset listing with
  filters, ETag revalidation.
- R3 Web replay UI: My games, the replay pane (controls, keyboard, autoplay,
  flip), FEN and PGN export and links, live SAN; the Playwright suite.
- R4 CLI and WASM replay: `games`, `pgn`, `replay` with its stepping
  commands, and a pager.
- R5, per user: PGN import (API, web, CLI, `db game import`) and deletion of
  stored games by their owner; schema v5.
- Accounts: password recovery by email (schema v4), administrative reset
  links, self-service account deletion. R8.3: a recovery request is answered
  before the lookup and mailed from a queue, at most one email per account
  per 5 minutes while its link is pending.
- Operations: transient write retries, degraded mode reported by `/health`,
  the integrity sweep (`-db-cleanup`), draw and resign flows.
- R6, in part: no cap on computer games; the engine queue takes two moves a
  worker and refuses more with 503, the game left `ongoing` for a retry;
  unfinished claimed games leave memory after a week idle (`-idle-game-ttl`);
  a request's 35-second deadline and the server's lifetime reach its storage
  calls, while queued writes keep the writer's own (the processor needs none:
  its writes are queued). A disconnect stays unnoticed: that needs net/http.

## Decisions

- D1 Account lifetime, done: site and CLI accounts are identical and never
  expire; registration closes at `-max-users`; a game without a registered
  player is deleted 24 hours after its last activity.
- D2 History visibility: a game's history and PGN are public to holders of
  its random ID; IDs are never listed publicly (R5.4 lists published archive
  games only).
- D3 SAN and PGN are derived on read, server-side in Go, from the initial
  FEN and the UCI line.
- D4 Player names, done: a claim snapshots the username, which survives
  renames and account deletion.
- D5 Draw rules, done: dead material, threefold repetition and the
  fifty-move rule apply automatically; offers and resignation exist;
  `termination` says how a game ended.
- D6 Undo after a result: allowed, so a stored game can be rewritten. A
  resignation or agreed draw between two humans is final, another user's
  claim refuses undo and reconfiguration (no takebacks between players), and
  the first concession stays on record.
- D7 List pagination, done: keyset cursor on `(start_time_utc, game_id)`;
  `offset` kept, exclusive with it.
- D8 Human against human in the web client: open, R9.3.
- D9 Replay without an account, done: the web client lists the games it
  created anonymously from `localStorage`, for the 24-hour retention.
- D10 Live SAN, done: the live game response carries `san`.
- D11 Replay history, done: views push browser history entries; stepping a
  replay replaces its entry.

## Web Client Constraints

The host's security headers bind every web client item (R5.4, R9.2, R9.3):

- `script-src 'self' 'wasm-unsafe-eval'`: code lives in `app.js`; no inline
  scripts, `on*` attributes, `eval` or `new Function`.
- `style-src 'self' 'unsafe-inline'`: styles in `style.css`, state classes
  rather than inline style strings.
- `img-src 'self' data:`: pieces stay Unicode glyphs or `data:` SVG.
- `connect-src 'self'`: every request goes to the same origin under
  `/chess/`; no CDN or cross-origin API.
- `frame-ancestors 'self'`: the embedding page stays on the same origin, with
  no `X-Frame-Options` beside it. Downloads from `blob:` URLs and the
  clipboard work in the same-origin frame; its sandbox needs
  `allow-downloads`.

## R5 Imported and Curated Games

A signed-in user imports one PGN game at a time into their own list
(`POST /api/games/import`): the strict parser `chess.ParsePGN` takes one
well-formed game, the rules core replays every move, and the result must
agree with the final position. What remains is a curated archive: games no
account owns, loaded in bulk and listed publicly.

### R5.1 Archive rights: what may be bundled

- Priority: P3, a gate rather than code: no collection is published before
  its rights are checked
- Affected files: none; each collection's rights are recorded on its row
  (R5.2)
- Guide: [Import a Game from PGN](api.md#import-a-game-from-pgn)

The moves of a game are widely held to be facts, but a compilation can carry
a database right (in the EU), annotations are authored text, and a source's
terms of use can forbid redistributing what it serves. For each collection,
before it is published:

- The source, its licence and the attribution it asks for, recorded with the
  collection.
- Whether its terms allow redistribution, bulk download included.
- Annotations: the importer drops comments and variations, keeping moves and
  tags only, so a collection whose value is its annotations is out of scope.

### R5.2 Archive model: games no account owns

- Priority: P3, not now: after R5.1 clears a first collection; schema v6
- Affected files: `internal/server/storage/schema.go`,
  `internal/server/storage/game.go`, `internal/server/storage/import.go`,
  `internal/server/service/import.go`, `internal/server/service/history.go`,
  `internal/server/replay/replay.go`, `doc/database.md`,
  `doc/architecture.md`
- Guide: [Database: Games](database.md#games),
  [Retention](architecture.md#retention)

A stored game belongs to the users who claimed its sides or to the account
that imported it (`imported_by`). `DeleteAnonymousGames` deletes a game with
neither a day after its last activity, so as the query stands an archive
game is gone the day after its import; `OrphanedGames` would follow if
archive games recorded an importer whose account is later deleted.

- A collection row (title, source URL, licence, attribution, published,
  featured order) and an archive row per game (collection, `WhiteElo`,
  `BlackElo`, `ECO`, content hash, featured order); per-game metadata stays
  off `games`.
- The purge and its partial index `games_anonymous_start_idx` exclude
  archive games.
- `GameRecord.Origin` infers played or imported from `imported_by`; a third
  origin is better stored as a kind than inferred from which owner column is
  set.
- Decide what unpublished means: absent from the public list only, its
  history and PGN still answering by ID as every game's do (D2), or 404.
- Exported PGN carries the archive tags (`WhiteElo`, `BlackElo`, `ECO`) and
  the collection's attribution; `db verify` and the integrity sweep cover
  archive games unchanged.

### R5.3 Bulk import: many games, idempotent

- Priority: P3, not now: after R5.2
- Affected files: `internal/server/chess/pgnread.go`,
  `internal/server/chess/pgnread_test.go`,
  `internal/server/chess/fuzz_test.go`, `internal/server/service/import.go`,
  `internal/server/storage/import.go`, `cmd/chess-server/cli/cli.go`,
  `doc/database.md`
- Guide: [Import a Game from PGN](api.md#import-a-game-from-pgn),
  [Database: Users](database.md#users)

`chess.ParsePGN` reads exactly one game, of at most 64 KiB and 1000 plies,
and `db game import` stores one file for one account. A collection is a PGN
database of thousands of games, or a directory of them:

- A streaming splitter that cuts a file into games and hands each to
  `ParsePGN`, so the per-game limits and strictness stay; fuzzed like
  `ParsePGN`. A bad game is reported with its index and skipped, not fatal.
- The per-game work of `Service.ImportGame` (replay, outcome, tag mapping)
  shared by both paths; games committed in batches, not a transaction each.
- Idempotency by content hash over the start FEN, the UCI line, the result
  and the identifying tags, unique in the archive table: a rerun stores only
  what is new.
- `db archive import -collection <name> -file <pgn> | -dir <dir> [-dry-run]`:
  a dry run parses and replays without writing; the summary per file counts
  stored, duplicate and rejected games, with the reasons.
- The API keeps one game per request; bulk loading is administrative.

### R5.4 Public archive: list endpoint and browsing

- Priority: P3, not now: after R5.2; R5.1 gates what is published
- Affected files: `internal/server/http/handler.go`,
  `internal/server/core/api.go`, `internal/server/service/history.go`,
  `internal/server/storage/game.go`,
  `internal/server/webserver/chess-client-web/` (`app.js`, `index.html`,
  `style.css`), `internal/client/api/client.go`,
  `internal/client/command/game.go`, `test/e2e/replay.spec.js`,
  `test/test-replay.sh`, `doc/api.md`
- Guide: [List My Stored Games](api.md#list-my-stored-games),
  [Get Durable Game History](api.md#get-durable-game-history),
  [Web Client Constraints](#web-client-constraints)

The one listing today is the signed-in user's own (`/api/users/me/games`);
history and PGN are public by ID (D2). The archive adds a public listing:

- `GET /api/archive?collection=&cursor=&limit=`: published games only, the
  keyset cursor of D7, at most 100 a page, no authentication. Archive games
  never change by undo, so a page can be `Cache-Control: public` with a
  `max-age`, and nginx can cache it if load asks for that.
- Filters, decided before the indexes: collection, ECO prefix, player name.
- Web: a Library view beside My games with the same rows (mini-board,
  players, result, ECO); replay and deep links (`?replay=<id>`) work as they
  are.
- CLI: `archive [collection]`, numbered for `replay <#>` as `games` is.

## R6 Live Games: Memory, Restarts and Outages

A live game is held in memory (`Service.games`) and mirrored to the database
through the ordered writer; memory is the authority while it is loaded.
After a restart, or a week idle, an unfinished game is replay-only, and once
storage degrades nothing is written until a restart. R6.2 and R6.3 share one
mapping, between a loaded `game.Game` and its rows.

### R6.2 Resume unfinished games after a restart

- Priority: P2: every deploy turns unfinished games replay-only, and games
  between people (R9.3) outlast a deploy cycle
- Affected files: `internal/server/service/game.go`,
  `internal/server/service/service.go`, `internal/server/game/game.go`,
  `internal/server/storage/game.go`, `internal/server/processor/processor.go`,
  `internal/server/webserver/chess-client-web/app.js`, `doc/api.md`,
  `doc/architecture.md`, `doc/development.md`
- Guide: [Durable Replay Read](architecture.md#durable-replay-read),
  [Get Game](api.md#get-game), [Retention](architecture.md#retention)

A lookup that misses `Service.games` answers `GAME_NOT_FOUND`, and after a
restart every lookup misses. The rows hold what a game needs to resume: the
initial FEN, each move with the FEN after it, the players with type, level,
search time, claim and name, the result, termination and concession.

- Load on a miss rather than at startup: read the row and moves behind the
  write barrier, as `GetGameHistory` does, outside `s.mu`; rebuild a
  `game.Game` and insert it under `s.mu`. Concurrent misses on one game load
  it once.
- What is not stored starts fresh: a standing draw offer and the per-side
  offer ply lapse, `LastResult` (score, depth) is empty, last activity is the
  last move's time. `pending` and `stuck` are not stored: a game whose
  computer was thinking resumes `ongoing` with the computer to move.
- Imported and archive games stay replay-only: they have no claims, so loaded
  live they could be moved by anyone holding the ID.
- No load while storage is degraded: the rows may lag memory (R6.3).
- A loaded game joins the set the purge and the integrity sweep already
  exclude; the idle unload (`-idle-game-ttl`) becomes lossless.
- Clients: the CLI's `join` works on a stored game unchanged; the web
  client's My games and device list offer Continue on an unfinished game
  where today they only replay it.

### R6.3 Recover from degraded storage without a restart

- Priority: P2, after R6.2, whose mapping it reuses: an outage longer than
  the retry window stops durable history until someone restarts the server
- Affected files: `internal/server/storage/storage.go`,
  `internal/server/storage/game.go`,
  `internal/server/storage/storage_test.go`,
  `internal/server/service/service.go`, `internal/server/service/game.go`,
  `doc/architecture.md`, `doc/api.md`, `doc/deployment.md`
- Guide: [Storage Layer](architecture.md#storage-layer-internalserverstorage),
  [Health Check](api.md#health-check),
  [Backups and restores](deployment.md#backups-and-restores)

The writer retries a transient failure for about three seconds. A COMMIT
error or a permanent error then latches `writeFailed` and `healthStatus`,
and a full queue latches `healthStatus` alone, for the life of the process:
every later write is refused at `enqueue`, history and listings answer 503,
and live play goes on in memory with nothing written. After a full queue the
writer still commits what was queued, while new writes are dropped.

Two ways out; pick one:

- Resync from memory: while degraded, record which loaded games changed; a
  probe on an interval finds the database back; each changed game is written
  whole in one transaction (upsert the row, delete moves past memory's last
  ply, insert the missing ones), the inverse of R6.2's load; then the latch
  clears. A game unloaded while degraded keeps only what was durable, as
  today.
- Fail closed: while degraded, refuse moves, undo, resignation, draws and new
  games with `STORAGE_UNAVAILABLE`, so memory cannot run ahead; the probe
  clears the latch, and only the game whose write failed is reloaded from its
  rows (R6.2) or unloaded.

Either way, a COMMIT error has an unknown outcome, so its game is re-read
rather than assumed, and `/health` reports both transitions. The storage
tests already terminate a backend and force a permanent failure; recovery
extends them.

## R7 Rules Core as the Move Authority

The rules core (`internal/server/chess`) decides custom starting positions,
the automatic draws, SAN and PGN, every imported move, and `db verify`.
Stockfish still decides the legality, mate and stalemate of live moves,
through one validation engine behind the processor's mutex, which every
human move in every game waits on; the core shadows each live move and logs
a disagreement.

### R7.1 Verify production data against the core

- Priority: P1, an operations step for whoever runs the server; R1's last
  test gate, and the gate of R7.2
- Affected files: none
- Guide: [Games](database.md#games),
  [Integrity sweep](database.md#integrity-sweep)

- `db verify` against production (`sudo chess-db verify` on Linux, the
  jail's `chess-server db verify` on FreeBSD): it only reads, so it can run
  in place, and a dump restored into a scratch database answers the same. A
  problem names the game and the ply.
- The shadow check's warnings in the server log over a few weeks of play:
  `rules core and engine disagree` (on legality or the resulting position)
  and `rules core cannot parse game position`. None is the gate.
- To keep checking: `-db-cleanup report` verifies 200 stored games an hour
  and only logs.
- A finding is a bug in the core or in stored data, and becomes a P0 item.

### R7.2 Validate human moves with the core

- Priority: P2, after R7.1: no engine round trip or global lock per human
  move, and no engine at all for games between humans
- Affected files: `internal/server/processor/processor.go`,
  `internal/server/processor/rules_test.go`, `doc/architecture.md`,
  `doc/stockfish.md`
- Guide: [Human Move](architecture.md#human-move-authenticated),
  [Concurrency](architecture.md#concurrency),
  [Synchronous Validation](stockfish.md#synchronous-validation-processor)

`handleMakeMove` sets the position on the validation engine, reads the new
FEN from Stockfish's `d` and probes for a reply with `go depth 1`, all under
`p.mu`; an illegal move shows as an unchanged FEN. `handleCreateGame`
classifies every new game the same way, games between humans included. `d`
is a debug command outside UCI, and the parser waits for its `Checkers:`
line, so a Stockfish release that changes it stops every move. The core has
what the job needs (`ParseUCI` against `LegalMoves`, `Play`, `InCheck`,
`HasLegalMoves`), with perft on six positions, a Stockfish cross-check of
about 6,000 positions a run, and fuzz targets.

- Human moves and game creation: legality, the resulting FEN, mate and
  stalemate from the core; `p.mu` then guards engine work only.
- The shadow in reverse for a release: the engine checks the core's decision
  off the request path and logs a disagreement.
- Computer moves: the core can re-validate the worker's move as well, which
  leaves the validation engine only the draw-offer evaluation.
- The shadow compares legality and the resulting FEN, not mate or
  stalemate; `db verify` (R7.1) covers that classification on stored games.
- Move latency and throughput measured before and after, as the PR rule for
  performance changes asks.

## R8 Accounts and Mail

Password recovery by email is built: `POST /api/auth/recover` stores a
one-time token (its SHA-256, for an hour) and mails a link through an SMTP
relay or, in development, a directory of `.eml` files. Addresses are taken
as typed.

### R8.1 Deliver mail through the mail jail

- Priority: P1, operations: recovery is built and tested, and off in
  production until a relay is set
- Affected files: none expected; `internal/server/mail/mail.go` and
  `cmd/chess-server/main.go` only if the relay's TLS needs it (below)
- Guide: [Password Recovery by Email](deployment.md#password-recovery-by-email),
  [Service](deployment.md#service-rcdchessd)

`rc.d/chessd` already turns `chessd_smtp_host`, `chessd_smtp_port`,
`chessd_smtp_user`, `chessd_smtp_password_file`, `chessd_mail_from` and
`chessd_public_url` into flags. What is left is the relay and the settings:

- The mail jail relays for the chess jail's address, unauthenticated inside
  the host or with a user and `smtp.secret`.
- The sender's domain publishes SPF for the relay's public address and the
  relay signs with DKIM, or reset mail lands in spam.
- TLS on the internal hop: the client upgrades with STARTTLS whenever the
  relay offers it and verifies the certificate against the system roots for
  the `-smtp-host` name, so a relay reached by IP address, or holding a
  certificate from a private CA, fails every send. Then reach it by the name
  on its certificate, or add a flag for the expected name or a CA file (a
  small change to `mail.SMTP`), or offer no STARTTLS on the internal port.
- Done when `/health` reports `"mail": "ok"` and a recovery from the web
  client completes.

### R8.2 Email verification

- Priority: P2, after R8.1, which it needs: a mistyped address hands the
  account's reset links to whoever owns it
- Affected files: `internal/server/storage/schema.go`,
  `internal/server/storage/reset.go`, `internal/server/storage/user.go`,
  `internal/server/service/user.go`, `internal/server/service/reset.go`,
  `internal/server/http/auth.go`, `cmd/chess-server/cli/cli.go`,
  `internal/server/webserver/chess-client-web/` (`app.js`, `index.html`),
  `internal/client/command/auth.go`, `test/e2e/replay.spec.js`,
  `doc/api.md`, `doc/database.md`
- Guide: [Register User](api.md#register-user),
  [Request a Password Reset](api.md#request-a-password-reset),
  [Users](database.md#users)

An address is stored as typed, at registration or by `db user set-email`,
and is unique; recovery mails a reset link to it. A typo hands the account's
reset links to whoever owns the typed address, and anyone can register an
address they do not own, which then refuses its owner's registration (409).
Email changes only through the CLI.

- `users.email_verified_at`, and a verification token made like the reset
  token (32 random bytes, SHA-256 stored, single use). `password_resets`
  becomes one token table with a kind column (reset, verify) rather than a
  parallel table per kind.
- `ResetPassword` consumes the token, then sets the password in a second
  transaction, so a server stopping between them spends the link: hash
  first, then consume the token and set the password in one transaction.
- Registration with an address mails the link; `POST /auth/verify` marks it;
  a resend for the signed-in user is rate-limited.
- Recovery mails only a verified address. Decide what an unverified address
  holds: uniqueness, as today, or nothing until verified (a partial unique
  index), which ends squatting but lets two accounts name the same
  unverified address.
- `db user set-email` leaves the new address unverified unless told
  otherwise; a self-service change, if wanted, verifies the new address
  before switching to it.
- Clients: the web client handles `?verify=<token>` as it does `?reset=`, the
  account dialog shows the state, and `/auth/me` carries it.
- Decide how the migration marks existing addresses: unverified, which is
  accurate, or verified, which spares current users a step.

### R8.4 Recovery timing through the sender

- Priority: P3, small: `/auth/register` already tells a taken username or
  email apart (409)
- Affected files: `internal/server/service/reset.go`
- Guide: [Request a Password Reset](api.md#request-a-password-reset)

One sender mails recovery requests in order, and an account with an email
costs it more work than one without. A caller who fills its queue (503) or
times their own reset mail can tell whether a username's account has an
email.

- Closing it needs the sender's work per request to be the same for every
  account.

## R9 Clients

The web client plays the computer and replays stored games; the terminal
client drives the whole API, natively and in the browser.

### R9.1 Interrupt long-running CLI commands

- Priority: P2, small and contained
- Affected files: `internal/client/command/replay.go`,
  `internal/client/command/game.go`, `internal/client/api/client.go`,
  `internal/client/session/session.go`, `cmd/chess-client-cli/main.go`, a
  native and browser pair in `internal/client/command` (as `pass_native.go`
  and `pass_wasm.go`), `web/chess-client-wasm/terminal.js`, `doc/client.md`
- Guide: [auto](client.md#auto), [poll](client.md#poll--p),
  [Limitations](client.md#limitations)

`auto` sleeps between plies to the end of the line, `poll` waits up to 30
seconds, and `computer` up to three polls. Natively Ctrl-C ends the client,
and with it the session, whose login and current game live in memory only.
In the browser, terminal.js passes keys to Go only while it waits on a read,
so nothing reaches a running command.

- One interrupt for a running command: natively `signal.NotifyContext`
  around it; in the browser terminal.js turns Ctrl-C outside a read into a
  call the Go side registered (`syscall/js`), which cancels the same context.
- `auto` checks it between plies; `poll` and `computer` pass it to their
  requests, so the API client gains calls that take a context.
- At the prompt Ctrl-C keeps today's behaviour.

### R9.2 Analysis on a replay

- Priority: P3, a decision first: which engine, and at whose cost
- Affected files: `internal/server/webserver/chess-client-web/` (`app.js`,
  `index.html`, `style.css`); for a server endpoint also
  `internal/server/engine/engine.go`, `internal/server/processor/queue.go`,
  `internal/server/http/handler.go`, `internal/server/core/api.go`,
  `doc/api.md`
- Guide: [Get Durable Game History](api.md#get-durable-game-history),
  [Application Usage](stockfish.md#application-usage),
  [Web Client Constraints](#web-client-constraints)

The replay pane shows stored positions and exports FEN and PGN; nothing
evaluates a position. Three ways, cheapest first:

- A link out: the position opened on an external analysis board (lichess's
  takes a FEN in its URL). No engine and no server cost; the game leaves the
  site only when the user follows the link, and `connect-src` does not
  govern a navigation.
- A server endpoint: an evaluation of a FEN with Stockfish (`MultiPV`, a
  depth or node budget) on its own worker, so it never delays computer
  moves; signed-in, rate-limited, cached by normalized FEN. A whole-game
  review (an evaluation per ply, blunders marked) is a search per ply: a
  background job with stored results, so a schema change.
- In the browser: a Stockfish WASM build served from the same origin
  (`'wasm-unsafe-eval'` allows it); single-threaded, since threads need
  cross-origin isolation that the embedding page would have to serve too; a
  full NNUE network weighs tens of megabytes; Stockfish is GPLv3, so its
  source is offered with the binary.

### R9.3 Human against human in the web client

- Priority: P3, a decision first (D8); after R6.2, or a deploy ends every
  game between people
- Affected files: `internal/server/processor/processor.go`,
  `internal/server/service/game.go`, `internal/server/http/handler.go`,
  `internal/server/core/api.go`,
  `internal/server/webserver/chess-client-web/` (`app.js`, `index.html`,
  `style.css`), `test/e2e/`, `doc/api.md`
- Guide: [Claims and control](api.md#claims-and-control),
  [Offer, accept, or decline a draw](api.md#offer-accept-or-decline-a-draw),
  [Long-Polling Flow](architecture.md#long-polling-flow)

Two-player games run through the API and the terminal client (`join`,
`poll`). The web client plays only the computer and long-polls only while
the computer thinks. A side is claimed by its first authenticated move; an
anonymous move claims nothing, so an unclaimed side is open to anyone with
the ID; and a signed-in creator of a game between humans always gets White.

- Decide who may play: signed-in players only, whose claims protect both
  sides, or anonymous seats too, which needs a new mechanism (a secret per
  side), since a claim is an account.
- Server: the creator picks a side; a join claims the open side for a
  signed-in user without a move and wakes long-polls. A game whose two sides
  are claimed already refuses takebacks and reconfiguration.
- Web: Play a friend in the new-game dialog, an invite link (`?game=<id>`),
  a join screen for the open side, a long-poll while the opponent is to
  move, an offer indicator on Draw (pressing it accepts), and the opponent's
  name in the turn strip.
- Tests: an end-to-end spec with two browser contexts playing a short game,
  an offer and a resignation.

## R10 Observability

### R10.1 Metrics

- Priority: P3, a decision first: whether a scraper exists; the logs carry
  most of this today
- Affected files: `internal/server/storage/storage.go`,
  `internal/server/service/user.go`, `internal/server/service/history.go`,
  `internal/server/service/service.go`, `internal/server/processor/queue.go`,
  `internal/server/processor/processor.go`, `cmd/chess-server/main.go`,
  `doc/architecture.md`, `doc/deployment.md`, `doc/deployment-linux.md`
- Guide: [Storage Layer](architecture.md#storage-layer-internalserverstorage),
  [Health Check](api.md#health-check)

`/health` reports storage, mail and the build; everything else is in the
log, where `-log-level debug` adds the queue depth and duration of each
write.

- The measures asked for: write-queue depth, retries, degraded transitions,
  flush and replay-read latency, KDF slot wait. Beside them: shadow-check
  disagreements (R7.1's gate as a counter rather than a log search), engine
  queue depth and engine restarts, loaded games and those with a computer,
  long-poll waiters.
- Form: the Prometheus text format written with the standard library
  (counters, gauges, fixed-bucket histograms) needs no dependency; `expvar`
  serves JSON without histograms.
- Exposure: a listener of its own (`-metrics-addr`, loopback or the jail's
  address), not the API port that nginx proxies.
