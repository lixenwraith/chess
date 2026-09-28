# Game Replay Plan

Target experience: a user registers, signs in, plays, and later opens **My
games** to pick any finished or unfinished game. Replay runs automatically or
one ply at a time for either side, moves backward and forward freely, and
exports the FEN or PGN of the position at the current ply, in the spirit of the
chess.com analysis viewer.

This plan starts from the PostgreSQL release and splits the remaining work
into phases that each ship and test on their own.

## Foundation in Place

- [x] PostgreSQL 18 schema with durable games, ordered moves, results, end
  times, and slot claims; transactional migrations (`schema_version`).
- [x] Each accepted move, first-move claim, and move-caused result commits in
  one transaction; undo removes moves and clears the result atomically.
- [x] `GET /api/games/{id}/history`: initial FEN plus UCI and resulting FEN
  for every ply, read in one REPEATABLE READ snapshot after pending writes.
- [x] `GET /api/users/me/games`: claimed games, newest first, with move
  count and final FEN (thumbnail) from a primary-key probe per game.
- [x] Claims survive player reconfiguration and terminal-game eviction; the
  listing indexes are ordered for keyset pagination.
- [x] Stable JWT key (`-jwt-secret-file`), so a returning user's session
  survives server restarts.
- [x] Uniform, non-expiring accounts for site and CLI users (D1); 24-hour
  retention of games without a registered player (D1).
- [x] Player-name snapshots on claim, returned in history and listings (D4).
- [x] Scripted FreeBSD jail deployment (`deploy/`).
- [x] Go client DTOs (`GetGameHistory`, `GetMyGames`) including `finalFen`.
- [x] Rules core (`internal/server/chess`, R1) and replay API (R2), below.
- [x] Pawn promotion in the web client (piece picker) and a specific API error
  for a promotion without its piece.

## Decisions

| # | Decision | Status |
|---|---|---|
| D1 | Account lifetime | **Done.** Site registrations and CLI-created accounts are identical and never expire; public registration closes at `-max-users`. Games without a registered player are deleted 24 hours after their last activity (`-anonymous-game-ttl`). |
| D2 | History visibility | **Decided: shareable by game ID.** The history endpoint stays public to holders of the 122-bit random ID; IDs are never listed publicly and the list endpoint stays authenticated. |
| D3 | Where SAN/PGN is produced | **Decided: server-side Go** (R1), derived on read from the initial FEN and the UCI line: no schema change, one implementation for web and CLI, no third-party script under the CSP. |
| D4 | Player names | **Done.** `white_name`/`black_name` snapshot the claimant's username in the claim transaction and survive renames and deletions; exposed as `players.*.name`. |
| D5 | Draw rules | **Done.** Draws apply automatically after every move, as on most online servers: dead material, threefold repetition, and the fifty-move rule (FIDE's claimable draws, and so also its 75-move and fivefold rules). Draw offers (a computer answers from its evaluation) and resignation exist; the stored `termination` says how a game ended. |
| D6 | Undo after a result | **Decided: keep allowed**, so a player can step back from a finished game and play a line again. Consequence: a finished game's stored history, result, and end time are rewritten by undo; replay shows the line as it stands now. |
| D7 | List pagination | **Done.** Opaque keyset `cursor` on `(start_time_utc, game_id)`; `offset` kept for existing clients and exclusive with `cursor`. |

## Phase R1 — Notation Core (server) — done

A dependency-free rules core in Go (`internal/server/chess`). Stockfish
remains the move validator for live play; the core notates stored games and
shadows the engine on every live move, logging any disagreement.

- [x] Position with strict FEN parse (one king per side, no pawns on the back
  ranks, side not to move not in check; castling rights without their rook
  dropped) and canonical serialization (en-passant square only when a capture
  is legal, so Stockfish's pseudo-legal convention normalizes away).
- [x] Legal move generation with castling paths, en passant, promotion, pins.
- [x] SAN encode (file/rank/square disambiguation, `+`/`#`, `O-O`, `=Q`) and
  lenient decode (`0-0`, `e8Q`, annotations, over-disambiguation).
- [x] PGN writer: Seven Tag Roster, supplemental tags, `SetUp`/`FEN` for custom
  starts with correct move numbering, 80-column movetext, escaped tag values.
- [x] Draw-rule helpers (D5) and `chess-server db verify`, which replays every
  stored game against the stored FENs and results.
- [x] Custom starting FENs validated by the core before the engine sees them.

Test gate:

- [x] Perft for the CPW start, Kiwipete, and positions 3–6 (depth 4; position
  3 to depth 5).
- [x] Engine cross-check: random games from all six positions, comparing the
  legal move set with Stockfish `go perft 1` and the FEN with Stockfish `d` at
  every ply (about 6,000 positions per run; skipped without Stockfish).
- [x] Fuzz targets: FEN parse round trip with SAN/UCI round trip of every
  legal move, SAN decoding, PGN tag escaping.
- [ ] `db verify` over a copy of the production database once it holds games.

## Phase R2 — Replay API — done

- [x] D7 keyset cursor, plus `status` and `color` filters on the list.
- [x] Additive history fields: `san` per move, `pgnResult`, `termination`.
- [x] `GET /api/games/{id}/pgn?ply=N` as an attachment; `db pgn` in the CLI.
- [x] Strong `ETag` with `Cache-Control: private, no-cache` on history and PGN
  (revalidate every time: D6 lets finished games change); `If-None-Match`
  returns 304.
- [x] Tests: rules-core unit, fuzz, and engine cross-check; service and
  handler tests against PostgreSQL; `test/test-replay.sh` over HTTP (SAN,
  PGN, ETag, promotion, FEN validation, cursor paging and filters).
- [x] Terminal client: `games` (cursor paging) and `pgn` commands.

## Phase R3 — Web Replay UI

Plain JavaScript in the embedded client, no framework.

- [ ] **My games** button beside the account indicator (authenticated only)
  opening a panel: mini-board from `finalFen`, result, date, opponent or
  engine level, move count; loading, empty, error, and "load more" states.
- [ ] Replay mode entered from the panel or a deep link
  `?replay=<gameId>&ply=<n>`; `history.pushState` so browser back/forward
  restores the list and the ply.
- [ ] Controls: first, previous, next, last; keyboard `←` `→` `Home` `End`;
  clickable SAN move list; autoplay with speed selector and pause; board flip;
  current ply announced through an `aria-live` region.
- [ ] Position rendering assigns the stored FEN for the ply; the browser never
  computes moves.
- [ ] Export at the current ply: copy FEN, copy PGN, and download PGN (fetched
  from R2 as a `Blob`, saved through a temporary `<a download>`). The live
  view's "Copy PGN" button copies UCI moves today; point it at the PGN
  endpoint too.
- [ ] Show SAN in the live move list (from the history, or a `san` field on
  the live game response).
- [ ] While replaying: stop live long-polling, disable move, undo, new-game,
  and player-configuration controls; **Back to live game** restores them.

Constraints from the host's security headers:

- `script-src 'self' 'wasm-unsafe-eval'`: all code in `app.js`; no inline
  scripts, `on*` attributes, `eval`, or `new Function`.
- `style-src 'self' 'unsafe-inline'`: styles belong in `style.css`; state
  classes instead of inline style strings.
- `img-src 'self' data:`: pieces remain Unicode glyphs or `data:` SVG.
- `connect-src 'self'`: all requests go to the same origin under `/chess/`
  (the existing `/config` fallback); no CDN or cross-origin API.
- `frame-ancestors 'self'` and `X-Frame-Options: SAMEORIGIN`: the iframe host
  page must stay on the same origin. Downloads from `blob:` URLs and
  `navigator.clipboard` work in a same-origin frame.

Test gate:

- [ ] Playwright (Chromium) end-to-end against server and PostgreSQL: play a
  short game, open My games, step backward and forward, autoplay to the end,
  and compare exported FEN/PGN with the API.
- [ ] Keyboard-only navigation and screen-reader labels; reduced-motion
  disables autoplay animation.
- [ ] Deep link works beneath `/projects/chess/` with the API at `/chess`.

## Phase R4 — CLI and WASM Replay (optional)

- [x] `games` (list with cursor paging) and `pgn [gameId] [ply]`.
- [ ] `replay <gameId|index>`, `next`, `prev`, `first`, `last`, `goto <ply>`,
  `auto <ms>`, `fen`, `pgn save <path>`.
- [ ] Replay state is separate from the live session: no polling, moves, undo,
  or configuration while replaying.

## Phase R5 — Curated Archive (later)

- [ ] Schema: game origin (`player`, `curated`), archive metadata (event, site,
  date, round, names, Elo, ECO, source URL, license, attribution), publication
  state, featured order; no user ownership.
- [ ] `chess-server db archive import <pgn|dir>` using the R1 SAN decoder:
  validate every move, import each game in one transaction, idempotent by
  content hash, dry-run and per-file summary.
- [ ] Public, bounded, cacheable list endpoint; replay through the same
  history/PGN endpoints.
- [ ] Verify redistribution rights for every bundled collection.

## Operations Carried Forward

- [ ] Metrics: write-queue depth, retries, degraded transitions, flush and
  replay latency, KDF wait time.
- [ ] Degraded-mode recovery without a restart (replay missing writes from
  memory or fail gameplay closed); today recovery requires a restart.
- [ ] Live-game restoration after restart (unfinished games are replay-only).
- [ ] Retention and user-initiated deletion or anonymization of games.
- [ ] Thread request contexts from HTTP handlers into storage calls; today each
  call carries its own deadline.
- [x] Draw and resign flows (D5) for live play.
- [x] A game row deleted by hand no longer degrades storage; optional
  integrity sweep (`-db-cleanup`) for broken or orphaned data.
- [ ] Once `db verify` and the live shadow check stay clean in production,
  consider validating human moves with the core instead of the engine: no
  engine round trip or lock per move, and Stockfish only for computer play.
