package replay

import (
	"testing"

	"github.com/lixenwraith/chess/internal/client/api"
)

func TestNavigationClampsAndSelectsStoredFEN(t *testing.T) {
	h := &api.GameHistoryResponse{
		InitialFEN: "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1",
		Moves: []api.HistoryMove{
			{MoveNumber: 1, MoveUCI: "e2e4", SAN: "e4", FENAfterMove: "fen1", PlayerColor: "w"},
			{MoveNumber: 2, MoveUCI: "c7c5", SAN: "c5", FENAfterMove: "fen2", PlayerColor: "b"},
			{MoveNumber: 3, MoveUCI: "g1f3", SAN: "Nf3", FENAfterMove: "fen3", PlayerColor: "w"},
		},
	}
	s := New(h)
	if s.Ply != 0 || s.FEN() != h.InitialFEN || s.Label(0) != "start" {
		t.Fatalf("new state: ply %d fen %q label %q", s.Ply, s.FEN(), s.Label(0))
	}
	if got := s.Next(); got != 1 || s.FEN() != "fen1" || s.Label(1) != "1. e4" {
		t.Errorf("next: ply %d fen %q label %q", got, s.FEN(), s.Label(1))
	}
	if got := s.Last(); got != 3 || !s.AtEnd() || s.Label(3) != "2. Nf3" {
		t.Errorf("last: ply %d atEnd %v label %q", got, s.AtEnd(), s.Label(3))
	}
	if got := s.Next(); got != 3 {
		t.Errorf("next past the end = %d", got)
	}
	if got := s.Goto(-5); got != 0 || s.FEN() != h.InitialFEN {
		t.Errorf("goto -5 = %d", got)
	}
	if got := s.Goto(2); got != 2 || s.Label(2) != "1... c5" || s.FEN() != "fen2" {
		t.Errorf("goto 2 = %d label %q", got, s.Label(2))
	}
	if got := s.Prev(); got != 1 {
		t.Errorf("prev = %d", got)
	}
	if s.Move(0) != nil || s.Move(4) != nil || s.Move(3).SAN != "Nf3" {
		t.Error("Move bounds")
	}
	want := "1. e4 c5 2. [Nf3]"
	s.Last()
	if got := s.Movetext(80); got != want {
		t.Errorf("movetext = %q, want %q", got, want)
	}
	if got := s.Movetext(8); got != "1. e4 c5\n2. [Nf3]" {
		t.Errorf("wrapped movetext = %q", got)
	}
	if s.Result() != "ongoing" {
		t.Errorf("result = %q", s.Result())
	}
	h.Result, h.PGNResult, h.Termination = "white_wins", "1-0", "checkmate"
	if s.Result() != "1-0 (checkmate)" {
		t.Errorf("result = %q", s.Result())
	}
}

func TestCustomStartNumbersFromItsMoveAndSide(t *testing.T) {
	h := &api.GameHistoryResponse{
		InitialFEN: "4k3/8/8/8/8/8/8/4K2R b K - 0 40",
		Moves: []api.HistoryMove{
			{MoveNumber: 1, MoveUCI: "e8d8", SAN: "Kd8", FENAfterMove: "f1", PlayerColor: "b"},
			{MoveNumber: 2, MoveUCI: "e1g1", SAN: "O-O", FENAfterMove: "f2", PlayerColor: "w"},
			{MoveNumber: 3, MoveUCI: "d8c8", SAN: "Kc8", FENAfterMove: "f3", PlayerColor: "b"},
		},
	}
	s := New(h)
	for ply, want := range map[int]string{1: "40... Kd8", 2: "41. O-O", 3: "41... Kc8"} {
		if got := s.Label(ply); got != want {
			t.Errorf("label %d = %q, want %q", ply, got, want)
		}
	}
	s.Goto(2)
	if got := s.Movetext(80); got != "40... Kd8 41. [O-O] Kc8" {
		t.Errorf("movetext = %q", got)
	}
	if w, b := s.Players(); w != "Anonymous" || b != "Anonymous" {
		t.Errorf("players = %q %q", w, b)
	}
	h.Players.White = api.PlayerInfo{Type: 2, Level: 7}
	h.Players.Black = api.PlayerInfo{Type: 1, Name: "alice"}
	if w, b := s.Players(); w != "Stockfish L7" || b != "alice" {
		t.Errorf("players = %q %q", w, b)
	}
	empty := New(&api.GameHistoryResponse{InitialFEN: h.InitialFEN})
	if empty.Movetext(80) != "(no moves)" || empty.Last() != 0 {
		t.Error("empty history")
	}
}
