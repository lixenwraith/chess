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
