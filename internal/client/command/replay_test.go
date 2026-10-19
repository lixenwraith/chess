package command

import (
	"testing"

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
