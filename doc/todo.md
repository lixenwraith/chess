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

## Decisions

| # | Decision | Status |
|---|---|---|
| D1 | Account lifetime | **Done.** Site registrations and CLI-created accounts are identical and never expire; public registration closes at `-max-users`. Games without a registered player are deleted 24 hours after their last activity (`-anonymous-game-ttl`). |
| D2 | History visibility | **Decided: shareable by game ID.** The history endpoint stays public to holders of the 122-bit random ID; IDs are never listed publicly and the list endpoint stays authenticated. |
| D3 | Where SAN/PGN is produced | **Decided: server-side Go** (R1), derived on read from the initial FEN and the UCI line: no schema change, one implementation for web and CLI, no third-party script under the CSP. |
| D4 | Player names | **Done.** `white_name`/`black_name` snapshot the claimant's username in the claim transaction and survive renames and deletions; exposed as `players.*.name`. |
| D5 | Draw rules | Open. Recommendation: detect insufficient material, threefold repetition, and the 50/75-move rules in the R1 core; add resign/draw offers later. PGN `Result` is `*` for unterminated games. |
| D6 | Undo after a result | **Decided: keep allowed**, so a player can step back from a finished game and play a line again. Consequence: a finished game's stored history, result, and end time are rewritten by undo; replay shows the line as it stands now. |
| D7 | List pagination | Open. Recommendation: keyset cursor on `(start_time_utc, game_id)`, keeping offset during v1; the claim indexes are already ordered for it. |

## Phase R1 — Notation Core (server, no API change)

A small, dependency-free chess core in Go (`internal/server/chess`), used by
replay only; Stockfish remains the move validator for live play.

- [ ] Board model with FEN parse/serialize (extend `internal/server/board`).
- [ ] Legal move generation: pseudo-legal moves plus king-safety filtering,
  castling rights and paths, en passant, promotion.
- [ ] SAN encode (disambiguation by file, rank, or both; `+`/`#`; `O-O`,
  `O-O-O`; `=Q`) and SAN decode for later PGN import.
- [ ] PGN writer: Seven Tag Roster (White/Black from the name snapshots, else
  "Anonymous" or "Stockfish level N"), `SetUp`/`FEN` tags for custom starts,
  `Termination`, 80-column movetext, `Result` from the stored outcome.
- [ ] Draw-rule helpers (D5) and an integrity check that replays the stored UCI
  line and compares each generated FEN with the stored `fenAfterMove`.

Test gate:

- [ ] Perft node counts for the standard CPW positions (start, Kiwipete, and
  positions 3–6) to depth 4, which exercises castling, en passant, promotion,
  and pins.
- [ ] Property tests over random legal games: SAN decode(encode(m)) == m; the
  generated FEN sequence is identical to one produced by Stockfish's `d`
  command for the same line.
- [ ] Fuzz targets for FEN parsing, SAN decoding, and PGN writing (no panics,
  bounded output).
- [ ] Integrity check run over every stored game in a production-shaped copy.

## Phase R2 — Replay API

- [ ] Implement D7 (keyset cursor) once decided.
- [ ] Additive history fields: `san` per move, `outcome`/`termination`, and
  `pgnResult`; keep existing fields unchanged.
- [ ] `GET /api/games/{id}/pgn?ply=N`: `application/x-chess-pgn`,
  `Content-Disposition: attachment; filename="chess-<date>-<id8>.pgn"`,
  moves 1..N (default all). The FEN at any ply is already in the history.
- [ ] `ETag` and `Cache-Control: private, max-age` for terminal games;
  `If-None-Match` returns 304.
- [ ] Filters on the list endpoint: result, color, ongoing/finished.
- [ ] Tests: handler tests against PostgreSQL (pgtest), golden PGN files for
  mate, stalemate, promotion, castling, en passant, custom FEN, and an ongoing
  game; shell-suite coverage for authorization (another user's list, expired
  session) and cursor stability under concurrent inserts.

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
  from R2 as a `Blob`, saved through a temporary `<a download>`).
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

- [ ] `games` (list with pagination), `replay <gameId|index>`, `next`, `prev`,
  `first`, `last`, `goto <ply>`, `auto <ms>`, `fen`, `pgn save <path>`.
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
- [ ] Draw and resign flows (D5) for live play, independent of replay.
