# Agent Notes

What earlier agents learned that neither AGENTS.md nor the other docs state.

- A handler's context is `c.Context()`: the first middleware sets it to the
  request's 35-second deadline within the server's lifetime. `c` is itself a
  `context.Context` that is never cancelled, and fasthttp's `c.RequestCtx()`
  ends only at shutdown, never on a disconnect.
- `app.Test(req, fiber.TestConfig{Timeout: d})` leaves `FailOnTimeout` false,
  unlike `app.Test(req)`: a timed-out request is not an error unless the
  config sets `FailOnTimeout: true` too.
- HTTP handler tests need no Stockfish when no tested path calls
  `proc.Execute`: `service.New(nil, secret)` and a nil processor serve them
  (`TestLongPollEndsWhenServerContextIsCancelled`); `processor.New` starts
  the engine.
- A test that sets a field the reset-mail sender reads (`mailer.limits`)
  sets it before the first `RequestPasswordReset`: the queue's send orders
  the write before the sender's read, which keeps `-race` quiet.
- To show a comment sweep changed no code, print each file without comments
  (`go/parser`, then `go/printer` with the comments dropped) at HEAD and in
  the tree, and diff the two ignoring blank lines.
- `test/run-test-server.sh` starts `bin/chess-server -dev` by a relative
  path, so a pattern on the checkout path misses it; a stale server keeps
  8080 and 9090 and the next suite runs against it. Stop it by PID.
- The Playwright suite registers accounts, so `--repeat-each` soon meets the
  5-a-minute registration limit: those 429s are not regressions.
- The Playwright suite, `test-replay.sh` and `test-draw.sh` each register
  accounts: run back to back they pass the 5-a-minute limit and the later one
  fails on 429. New e2e tests sign in as `alice` or `bob`, which
  `run-test-server.sh` creates.
- `core.Player` holds a slice (`Seat`): compare players with
  `reflect.DeepEqual`, not `==`.
- A storage test that renames a user after queueing a claim flushes first:
  the queued write takes the name when it runs.
- The operator keeps the database across releases: a schema change is a
  migration over the rows it finds, never a fresh start. Rehearse it: seed a
  schema through the previous release's server, then open it with the new
  build and run `db verify`.
- Docs give a database command as `chess-server db <command>` and say once
  how each system runs it (database.md, The Server's `db` Commands):
  `chess-db` is no program but the Linux installer's wrapper, running it as
  `chess` with the DSN the service runs with, which this repository cannot
  know.
- A fuzz run that sits at 0 execs a second is minimizing an input, by
  default for a minute each: `-fuzzminimizetime=2s` shows the real rate.
- Account deletion clears `imported_by`, so an import outlives its owner
  until the purge: `games.origin` says how a game was stored, not who holds
  it.
- `applyURL` awaits a game's load: two history steps in a row overlap, and
  the later one must win (`ui.navigation`).
- Sending mail is `github.com/lixenwraith/mail`'s work: a change to how a
  message is rendered or delivered lands there, as one to hashing lands in
  `lixenwraith/auth`. What chess mails, to whom and how often is
  `service/reset.go`.
- To write `go.sum` lines for a module version that has no tag yet, serve
  its branch from a file proxy: `git archive --prefix=<module>@<version>/`
  as `@v/<version>.zip` beside `.mod`, `.info` and `list`, then `go mod
  tidy` with `GOPROXY=file://<dir>,...` and `GONOSUMDB=<module>`. The hash
  is of the tree, so it holds once that tree is tagged.
