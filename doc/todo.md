# Roadmap

What is left to build, in batches of numbered items: R5, R7 and R8 hold what
is open, the other batches being done. Each item names its priority, the
files it touches and the docs that explain the area. A finished item leaves
this file, and the line of its batch under Done says what it delivered.

- P0: a defect in production now; the next PR.
- P1: small, and it unblocks other items or turns on shipped work.
- P2: planned next; its prerequisites are met or named.
- P3: not now: it needs a decision or other items first.

## Order of Work

- Phase 1, now:
  - R8.1 P1: deliver mail through the mail jail (operations)
- Phase 2, next, in this order:
  - R8.2 P2: email verification, after R8.1
- Phase 3, later, each after its decision:
  - R8.4 P3: recovery timing through the sender
  - R7.3 P3: retire the engine shadow, after a release without a finding
  - R5.5 P3: partial dates of games read from PGN, with the first old collection

## Done

- R1 Notation core: `internal/server/chess` (strict FEN, legal moves, SAN,
  PGN, draw rules) and `db verify`; perft, a Stockfish cross-check and fuzz
  targets.
- R2 Replay API: history with SAN, PGN export (`?ply=`), keyset listing with
  filters, ETag revalidation.
- R3 Web replay UI: My games, the replay pane (controls, keyboard, autoplay,
  flip), FEN and PGN export and links, live SAN; the Playwright suite.
- R4 CLI and WASM replay: `games`, `pgn`, `replay` with its stepping
  commands, and a pager.
- R5, per user: PGN import (API, web, CLI, `db game import`) and deletion of
  stored games by their owner; schema v5.
- R5 Curated archive: collections of games no account owns (schema v7:
  `games.origin`, `collections`, `archive_games`); `db archive import` reads
  PGN databases through a streaming splitter, a batch a transaction, a game
  stored once by its content hash, a refused game skipped with its place;
  a collection is published only with its source, licence and attribution;
  `GET /api/archive` lists published games by keyset cursor with collection,
  opening and player filters; the web client's Library and the terminal
  client's `archive` browse it; exported PGN carries the archive tags and
  the collection's rights. Nothing is bundled.
- Accounts: password recovery by email (schema v4), administrative reset
  links, self-service account deletion. R8.3: a recovery request is answered
  before the lookup and mailed from a queue, at most one email per account
  per 5 minutes while its link is pending.
- Operations: transient write retries, degraded mode reported by `/health`,
  the integrity sweep (`-db-cleanup`), draw and resign flows.
- R6 Live games: no cap on computer games; the engine queue takes two moves
  a worker and refuses more with 503, the game left `ongoing` for a retry;
  unfinished claimed games leave memory after a week idle (`-idle-game-ttl`);
  a request's 35-second deadline and the server's lifetime reach its storage
  calls, while queued writes keep the writer's own. A disconnect stays
  unnoticed: that needs net/http. An unfinished game played here loads from
  its rows on its next request, so a restart or the idle unload loses no
  game; the web client's lists offer Continue. Degraded storage recovers
  without a restart: the games whose writes were lost are written whole from
  memory once the database answers.
- R7 Move authority: the rules core decides legality, the resulting
  position, mate and stalemate for every move and every new game, so a human
  move takes no engine round trip and no lock, and a game between humans no
  engine. Stockfish replays each ruling off the request path and logs a
  disagreement (R7.3 retires it).
- R9 Clients: Ctrl-C stops a running `auto`, `poll` or `computer` in the
  terminal client, natively and in the browser, and the session stays. The
  web client plays a friend: the creator picks a side, an invite link
  (`?game=<id>`) offers the open one, a join takes it without a move, the
  game is watched by long-poll, Draw shows and accepts a standing offer, and
  the turn strip names the opponent. Schema v6. A signed-in viewer turns the
  Engine on in a replay: the evaluation and best three lines of the position
  shown, from `POST /api/analysis` (D13).

- R10 Observability: the measures (queues, retries, degraded transitions,
  flush, replay-read and KDF-slot latency, loaded games, long-poll waiters,
  engine restarts, shadow disagreements) as one `msg=metrics` log line every
  `-metrics-interval`, which LogWisp streams; no listener and no scraper.

## Decisions

- D1 Account lifetime, done: site and CLI accounts are identical and never
  expire; registration closes at `-max-users`; a game without a registered
  player is deleted 24 hours after its last activity.
- D2 History visibility: a game's history and PGN are public to holders of
  its random ID; IDs are never listed publicly, the games of published
  archive collections aside.
- D3 SAN and PGN are derived on read, server-side in Go, from the initial
  FEN and the UCI line.
- D4 Player names, done: a claim snapshots the username, which survives
  renames and `db user delete`; self-service deletion clears it.
- D5 Draw rules, done: dead material, threefold repetition and the
  fifty-move rule apply automatically; offers and resignation exist;
  `termination` says how a game ended.
- D6 Undo after a result: allowed, so a stored game can be rewritten. A
  resignation or agreed draw between two humans is final, another user's
  claim refuses undo and reconfiguration (no takebacks between players), and
  the first concession stays on record.
- D7 List pagination, done: keyset cursor on `(start_time_utc, game_id)`;
  `offset` kept, exclusive with it.
- D8 Human against human in the web client, done: signed-in players hold a
  side by their claim and guests by a seat, a secret per side shown once,
  stored as its SHA-256 and sent as `X-Chess-Seat`.
- D9 Replay without an account, done: the web client lists the games it
  created anonymously from `localStorage`, for the 24-hour retention.
- D10 Live SAN, done: the live game response carries `san`.
- D11 Replay history, done: views push browser history entries; stepping a
  replay replaces its entry.
- D12 Move authority, done: the rules core, without R7.1's check of the
  stored games as a gate: a stored game is verified before it resumes and
  stays replay-only when it fails; `db verify` checks them all at any time
  (R7.3).
- D13 Analysis, done: a server endpoint that searches one position within a
  node budget, on an engine of its own, signed-in, rate-limited and cached by
  position. No whole-game review (a search per ply: a background job with
  stored results) and no engine in the browser.
- D14 Archive, done: an unpublished collection is absent from the public
  list only, its games' history and PGN answering by ID as every game's do
  (D2); the list filters by collection, opening-code prefix and
  player-name prefix on plain indexes, the names lowercased by the server.

## Web Client Constraints

The host's security headers bind every change to the web client:

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

A signed-in user imports one PGN game into their own list, and an
administrator loads PGN databases into the archive's collections; both read
a game with the strict parser and replay it by the rules.

### R5.5 Partial dates of games read from PGN

- Priority: P3, with the first collection of old games
- Affected files: `internal/server/storage/schema.go`,
  `internal/server/service/import.go`, `internal/server/service/archive.go`,
  `internal/server/replay/replay.go`,
  `internal/server/webserver/chess-client-web/app.js`, `doc/api.md`
- Guide: [Import a Game from PGN](api.md#import-a-game-from-pgn),
  [Browse the Archive](api.md#browse-the-archive)

A PGN `Date` may leave parts unknown (`1858.??.??`). The import stores a
timestamp: an unknown month or day becomes 01 and an unknown year the time
of the import, so lists, replays and exported PGN show a day the source did
not give, and an undated game sorts as the newest.

- Keep the tag's text beside the timestamp: show and export the text, sort
  by the timestamp.
- Decide where an undated game sorts.

## R7 Rules Core as the Move Authority

The rules core (`internal/server/chess`) decides every live move, custom
starting positions, the automatic draws, SAN and PGN, every imported move,
and `db verify`. Stockfish plays the computer's moves, answers draw offers,
and replays each ruling of the core as a shadow.

### R7.3 Retire the engine shadow

- Priority: P3, after a release whose log holds no `rules core and engine
  disagree` and no `rules core cannot parse game position`, and a `db verify`
  of the stored games without a problem
- Affected files: `internal/server/processor/processor.go`,
  `internal/server/processor/rules_test.go`,
  `internal/server/engine/engine.go`, `doc/architecture.md`,
  `doc/stockfish.md`
- Guide: [Human Move](architecture.md#human-move-authenticated),
  [Shadow Check](stockfish.md#shadow-check-and-draw-offers-processor)

The shadow compares legality and the resulting FEN of every ruling, not mate
or stalemate; `db verify` and `-db-cleanup report` cover that classification
on stored games. It reads the position through `d`, a debug command outside
UCI.

- A finding is a bug in the core, and becomes a P0 item.
- Without one: remove the shadow loop, its backlog and `engine.Diagnose`;
  the processor's engine then only answers draw offers.

## R8 Accounts and Mail

Password recovery by email is built: `POST /api/auth/recover` stores a
one-time token (its SHA-256, for an hour) and mails a link through an SMTP
relay or, in development, a directory of `.eml` files. Addresses are not
verified.

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

An address is stored unverified (lowercased), at registration or by `db user
set-email`, and is unique; recovery mails a reset link to it. A typo hands the account's
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
