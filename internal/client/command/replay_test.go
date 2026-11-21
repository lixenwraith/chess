package command

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lixenwraith/chess/internal/client/api"
	"github.com/lixenwraith/chess/internal/client/replay"
	"github.com/lixenwraith/chess/internal/client/session"
)

func TestPGNSaveArgs(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		path string
		rest []string
		err  bool
	}{
		"none":         {[]string{"abc", "3"}, "", []string{"abc", "3"}, false},
		"save last":    {[]string{"abc", "save", "out.pgn"}, "out.pgn", []string{"abc"}, false},
		"save first":   {[]string{"save", "out.pgn", "abc", "3"}, "out.pgn", []string{"abc", "3"}, false},
		"missing path": {[]string{"save"}, "", nil, true},
	} {
		path, rest, err := pgnSaveArgs(tc.args)
		if (err != nil) != tc.err || path != tc.path || len(rest) != len(tc.rest) {
			t.Errorf("%s: path %q rest %v err %v", name, path, rest, err)
			continue
		}
		for i := range rest {
			if rest[i] != tc.rest[i] {
				t.Errorf("%s: rest = %v, want %v", name, rest, tc.rest)
			}
		}
	}
}

func TestReplayRefusesLiveCommandsAndPrompt(t *testing.T) {
	s := &session.Session{APIBaseURL: "http://127.0.0.1:1", Client: api.New("http://127.0.0.1:1")}
	r := NewRegistry(s)
	if ReplayPrompt(s) != "" {
		t.Error("prompt segment while live")
	}
	s.Replay = replay.New(&api.GameHistoryResponse{
		GameID: "g", InitialFEN: "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1",
		Moves: []api.HistoryMove{{MoveNumber: 1, MoveUCI: "e2e4", SAN: "e4", FENAfterMove: "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq - 0 1", PlayerColor: "w"}},
	})
	if got := ReplayPrompt(s); got != " replay 0/1" {
		t.Errorf("prompt segment = %q", got)
	}
	// A live command is refused without touching the client (no server).
	r.Execute("move e2e4")
	r.Execute("next")
	if s.Replay.Ply != 1 || ReplayPrompt(s) != " replay 1/1" {
		t.Errorf("after next: ply %d", s.Replay.Ply)
	}
	r.Execute("live")
	if s.Replay != nil {
		t.Error("live did not end the replay")
	}
	for _, name := range []string{"new", "join", "move", "computer", "undo", "delete", "poll", "resign", "draw"} {
		if !liveOnly[name] {
			t.Errorf("%s is not refused while replaying", name)
		}
	}
}

func TestInterruptStopsAutoAtItsPlyAndPollWithoutAnError(t *testing.T) {
	// A server that never answers a long-poll within the test.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	s := &session.Session{APIBaseURL: server.URL, Client: api.New(server.URL), CurrentGame: "g"}
	s.Replay = replay.New(&api.GameHistoryResponse{
		GameID: "g", InitialFEN: "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1",
		Moves: []api.HistoryMove{{MoveNumber: 1, MoveUCI: "e2e4", SAN: "e4", FENAfterMove: "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq - 0 1", PlayerColor: "w"}},
	})

	// Each command is interrupted while it waits.
	interrupted := func() context.Context {
		ctx, interrupt := context.WithCancel(t.Context())
		time.AfterFunc(50*time.Millisecond, interrupt)
		return ctx
	}
	started := time.Now()
	if err := autoHandler(interrupted(), s, []string{"60000"}); err != nil || s.Replay.Ply != 0 {
		t.Errorf("interrupted auto: ply %d, %v", s.Replay.Ply, err)
	}
	if err := pollHandler(interrupted(), s, nil); err != nil || s.CurrentGame != "g" {
		t.Errorf("interrupted poll: game %q, %v", s.CurrentGame, err)
	}
	if waited := time.Since(started); waited < 100*time.Millisecond || waited > 5*time.Second {
		t.Errorf("the two interrupts took %v", waited)
	}
}
