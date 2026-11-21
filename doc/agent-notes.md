# Agent Notes

What earlier agents learned that neither AGENTS.md nor the other docs state.

- A handler's context is `c.UserContext()`: the first middleware sets it to
  the request's 35-second deadline within the server's lifetime. Fiber's
  `c.Context()` is fasthttp's `RequestCtx`, whose `Done` closes only at
  shutdown, never on a disconnect.
- HTTP handler tests need no Stockfish when no tested path calls
  `proc.Execute`: `service.New(nil, secret)` and a nil processor serve them
  (`TestLongPollEndsWhenServerContextIsCancelled`); `processor.New` starts
  the engine.
- A test that sets a field the reset-mail sender reads (`mailer.cooldown`)
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
