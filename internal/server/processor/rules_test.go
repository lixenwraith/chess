package processor

import (
	"bytes"
	"errors"
	"log/slog"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/lixenwraith/chess/internal/server/chess"
	"github.com/lixenwraith/chess/internal/server/core"
	"github.com/lixenwraith/chess/internal/server/game"
	"github.com/lixenwraith/chess/internal/server/service"
)

func TestPlay(t *testing.T) {
	const promotion = "8/5KP1/5n2/p3k3/1p3p2/1P6/P7/8 w - - 4 55"
	if _, err := play(promotion, "g7g8"); !errors.Is(err, chess.ErrPromotionRequired) {
		t.Errorf("g7g8: error = %v, want ErrPromotionRequired", err)
	}
	if pos, err := play(promotion, "g7g8q"); err != nil || pos.FEN() != "6Q1/5K2/5n2/p3k3/1p3p2/1P6/P7/8 b - - 0 55" {
		t.Errorf("g7g8q: %v, %v", pos, err)
	}
	if _, err := play(chess.StartFEN, "e2e5"); !errors.Is(err, chess.ErrIllegalMove) {
		t.Errorf("e2e5: error = %v, want ErrIllegalMove", err)
	}
	if _, err := play("not a fen", "e2e4"); !errors.Is(err, chess.ErrInvalidFEN) {
		t.Errorf("bad FEN: error = %v, want ErrInvalidFEN", err)
	}
}

// TestGameBetweenHumansNeedsNoEngine plays through a processor that has none:
// the rules alone accept, refuse and end the game.
func TestGameBetweenHumansNeedsNoEngine(t *testing.T) {
	svc, err := service.New(nil, []byte("test-secret-test-secret-test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	p := &Processor{svc: svc}
	human := core.PlayerConfig{Type: core.PlayerHuman}
	create := func(fen string) core.GameResponse {
		t.Helper()
		resp := p.Execute(t.Context(), NewCreateGameCommand(core.CreateGameRequest{White: human, Black: human, FEN: fen}))
		if !resp.Success {
			t.Fatalf("create %q: %+v", fen, resp.Error)
		}
		return resp.Data.(core.GameResponse)
	}
	move := func(gameID, uci string) ProcessorResponse {
		return p.Execute(t.Context(), NewMakeMoveCommand(gameID, core.MoveRequest{Move: uci}))
	}

	game := create("")
	for uci, code := range map[string]string{"e2e5": core.ErrInvalidMove, "e7e5": core.ErrInvalidMove} {
		if resp := move(game.GameID, uci); resp.Success || resp.Error.Code != code {
			t.Errorf("%s: %+v, want %s", uci, resp.Error, code)
		}
	}
	var last core.GameResponse
	for _, uci := range []string{"f2f3", "e7e5", "g2g4", "d8h4"} {
		resp := move(game.GameID, uci)
		if !resp.Success {
			t.Fatalf("%s: %+v", uci, resp.Error)
		}
		last = resp.Data.(core.GameResponse)
	}
	if last.State != "black wins" || last.Termination != "checkmate" || last.SAN[3] != "Qh4#" {
		t.Errorf("fool's mate ended %q by %q, SAN %v", last.State, last.Termination, last.SAN)
	}

	if stalemate := create("7k/5Q2/6K1/8/8/8/8/8 b - - 0 1"); stalemate.State != "stalemate" {
		t.Errorf("stalemated start is %q", stalemate.State)
	}
	if resp := p.Execute(t.Context(), NewCreateGameCommand(core.CreateGameRequest{
		White: human, Black: human, FEN: "4k3/8/8/8/8/8/8/R3K2R w KQ - 0 1 extra",
	})); resp.Success || resp.Error.Code != core.ErrInvalidFEN {
		t.Errorf("malformed FEN: %+v", resp)
	}
}

// TestSideOfACreateOrJoinIsHeldByItsTaker: a guest gets the secret of the side
// it takes in that one answer, and the side then moves only for it; a
// signed-in joiner claims by name.
func TestSideOfACreateOrJoinIsHeldByItsTaker(t *testing.T) {
	svc, err := service.New(nil, []byte("test-secret-test-secret-test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	p := &Processor{svc: svc}
	human := core.PlayerConfig{Type: core.PlayerHuman}
	run := func(cmd Command, actor core.Actor) (core.GameResponse, string) {
		cmd.Actor = actor
		resp := p.Execute(t.Context(), cmd)
		if !resp.Success {
			return core.GameResponse{}, resp.Error.Code
		}
		return resp.Data.(core.GameResponse), ""
	}

	created, code := run(NewCreateGameCommand(core.CreateGameRequest{White: human, Black: human, Side: "b"}), core.Actor{})
	if code != "" || created.Side != "b" || created.Seat == "" || !created.Players.Black.Seated || created.Players.White.Seated {
		t.Fatalf("guest creates as black: %+v %s", created, code)
	}
	guest := core.Actor{Seat: service.SeatHash(created.Seat)}
	if shown, _ := run(NewGetGameCommand(created.GameID), guest); shown.Seat != "" || shown.Side != "" {
		t.Errorf("the seat is shown again: %+v", shown)
	}
	move := func(uci string, actor core.Actor) string {
		_, code := run(NewMakeMoveCommand(created.GameID, core.MoveRequest{Move: uci}), actor)
		return code
	}
	if code := move("e2e4", core.Actor{}); code != "" {
		t.Fatalf("the open side refused a move: %s", code)
	}
	if code := move("e7e5", core.Actor{}); code != core.ErrUnauthorized {
		t.Errorf("the seated side moved without its seat: %q", code)
	}
	if code := move("e7e5", guest); code != "" {
		t.Errorf("the seated side refused its guest: %s", code)
	}

	alice := core.Actor{UserID: "0b8f2f0c-8a53-4d11-9f0e-1b1a3c1d2e3f", Name: "alice"}
	joined, code := run(NewJoinGameCommand(created.GameID, core.JoinRequest{}), alice)
	if code != "" || joined.Side != "w" || joined.Seat != "" || joined.Players.White.ClaimedBy != alice.UserID ||
		joined.Players.White.Name != "alice" {
		t.Fatalf("user joins the open side: %+v %s", joined, code)
	}
	if _, code := run(NewJoinGameCommand(created.GameID, core.JoinRequest{}), core.Actor{}); code != core.ErrConflict {
		t.Errorf("join with no side open: %q, want %s", code, core.ErrConflict)
	}
	computer := core.PlayerConfig{Type: core.PlayerComputer, Level: 1, SearchTime: 100}
	if _, code := run(NewCreateGameCommand(core.CreateGameRequest{White: human, Black: computer, Side: "black"}), core.Actor{}); code != core.ErrInvalidRequest {
		t.Errorf("taking the computer's side: %q, want %s", code, core.ErrInvalidRequest)
	}
}

// TestShadowLogsOnlyDisagreement: the engine's position after a move is held
// against the ruling, whatever en-passant convention it prints.
func TestShadowLogsOnlyDisagreement(t *testing.T) {
	var log bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))
	defer slog.SetDefault(previous)

	const afterE4 = "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq - 0 1"
	const engineAfterE4 = "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1"
	for name, tc := range map[string]struct {
		check     shadowCheck
		engineFEN string
		want      string
	}{
		"agree, legal":           {shadowCheck{fen: chess.StartFEN, uci: "e2e4", ruled: afterE4}, engineAfterE4, ""},
		"agree, illegal":         {shadowCheck{fen: chess.StartFEN, uci: "e2e5"}, chess.StartFEN, ""},
		"engine refuses":         {shadowCheck{fen: chess.StartFEN, uci: "e2e4", ruled: afterE4}, chess.StartFEN, "disagree on legality"},
		"engine accepts":         {shadowCheck{fen: chess.StartFEN, uci: "e2e5"}, engineAfterE4, "disagree on legality"},
		"another position":       {shadowCheck{fen: chess.StartFEN, uci: "e2e4", ruled: afterE4}, "rnbqkbnr/pppppppp/8/8/3P4/8/PPP1PPPP/RNBQKBNR b KQkq - 0 1", "disagree on resulting position"},
		"unreadable to the core": {shadowCheck{fen: chess.StartFEN, uci: "e2e4", ruled: afterE4}, "not a fen", "cannot parse game position"},
	} {
		log.Reset()
		tc.check.compare(tc.engineFEN)
		if got := log.String(); (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: logged %q, want %q", name, got, tc.want)
		}
	}
}

func TestNotateLine(t *testing.T) {
	line, err := chess.Replay(chess.StartFEN, []string{"e2e4", "e7e5", "g1f3", "b8c6", "f1b5"})
	if err != nil {
		t.Fatal(err)
	}
	fens := make([]string, 6)
	for i := range fens {
		fens[i] = line.Position(i).FEN()
	}
	moves := []string{"e2e4", "e7e5", "g1f3", "b8c6", "f1b5"}
	if got := strings.Join(notateLine(fens, moves), " "); got != "e4 e5 Nf3 Nc6 Bb5" {
		t.Errorf("SAN = %q", got)
	}
	// A ply the core cannot notate leaves only its own entry empty.
	broken := append([]string(nil), fens...)
	broken[2] = "not a fen"
	if got := notateLine(broken, moves); !reflect.DeepEqual(got, []string{"e4", "e5", "", "Nc6", "Bb5"}) {
		t.Errorf("SAN with a broken position = %q", got)
	}
	if got := notateLine(nil, nil); len(got) != 0 {
		t.Errorf("SAN of no moves = %q", got)
	}
	if got := notateLine(fens[:1], moves[:2]); !reflect.DeepEqual(got, []string{"e4", ""}) {
		t.Errorf("SAN short of positions = %q", got)
	}
}

func TestOutcome(t *testing.T) {
	cycle := []string{"g1f3", "g8f6", "f3g1", "f6g8"}
	line, err := chess.Replay(chess.StartFEN, append(cycle, cycle...))
	if err != nil {
		t.Fatal(err)
	}
	fens := make([]string, 9)
	for i := range fens {
		fens[i] = line.Position(i).FEN()
	}

	for name, tc := range map[string]struct {
		history []string
		fen     string
		state   core.State
		want    core.Termination
	}{
		"start":             {nil, chess.StartFEN, core.StateOngoing, core.TermNone},
		"white mated":       {nil, "rnb1kbnr/pppp1ppp/8/4p3/6Pq/5P2/PPPPP2P/RNBQKBNR w KQkq - 1 3", core.StateBlackWins, core.TermCheckmate},
		"black mated":       {nil, "R5k1/5ppp/8/8/8/8/8/4K3 b - - 0 1", core.StateWhiteWins, core.TermCheckmate},
		"stalemate":         {nil, "7k/5Q2/6K1/8/8/8/8/8 b - - 0 1", core.StateStalemate, core.TermStalemate},
		"mate before draws": {nil, "R5k1/5ppp/8/8/8/8/8/4K3 b - - 100 90", core.StateWhiteWins, core.TermCheckmate},
		"bare kings":        {nil, "4k3/8/8/8/8/8/8/4K3 w - - 0 1", core.StateDraw, core.TermInsufficientMaterial},
		"knight":            {nil, "4k3/8/8/8/8/8/8/3NK3 b - - 3 40", core.StateDraw, core.TermInsufficientMaterial},
		"rook":              {nil, "4k3/8/8/8/8/8/8/R3K3 w - - 3 40", core.StateOngoing, core.TermNone},
		"fifty moves":       {nil, "4k3/8/8/8/8/8/8/R3K3 w - - 100 90", core.StateDraw, core.TermFiftyMoveRule},
		"forty-nine":        {nil, "4k3/8/8/8/8/8/8/R3K3 w - - 99 90", core.StateOngoing, core.TermNone},
		"second occurrence": {fens[:4], fens[4], core.StateOngoing, core.TermNone},
		"third occurrence":  {fens[:8], fens[8], core.StateDraw, core.TermThreefoldRepetition},
	} {
		pos, err := chess.ParseFEN(tc.fen)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if state, termination := outcome(tc.history, pos); state != tc.state || termination != tc.want {
			t.Errorf("%s: got %v %q, want %v %q", name, state, termination, tc.state, tc.want)
		}
	}
}

func TestActingColor(t *testing.T) {
	human := func(claimedBy string) *core.Player {
		return &core.Player{Type: core.PlayerHuman, ClaimedBy: claimedBy}
	}
	computer := &core.Player{Type: core.PlayerComputer}
	view := func(white, black *core.Player) game.View {
		return game.View{WhitePlayer: white, BlackPlayer: black}
	}

	for name, tc := range map[string]struct {
		view      game.View
		requested string
		user      string
		want      core.Color
		code      string
	}{
		"only human side":           {view(human(""), computer), "", "", core.ColorWhite, ""},
		"only human side, black":    {view(computer, human("")), "", "", core.ColorBlack, ""},
		"claimed side of caller":    {view(human("u1"), human("")), "", "u1", core.ColorWhite, ""},
		"explicit color":            {view(human(""), human("")), "b", "", core.ColorBlack, ""},
		"explicit long color":       {view(human(""), human("")), "white", "", core.ColorWhite, ""},
		"ambiguous hot seat":        {view(human(""), human("")), "", "", 0, core.ErrInvalidRequest},
		"self-play needs color":     {view(human("u1"), human("u1")), "", "u1", 0, core.ErrInvalidRequest},
		"computer only":             {view(computer, computer), "", "", 0, core.ErrInvalidRequest},
		"computer side requested":   {view(human(""), computer), "b", "", 0, core.ErrInvalidRequest},
		"claimed by another":        {view(human("u1"), computer), "", "u2", 0, core.ErrUnauthorized},
		"claimed, anonymous caller": {view(human("u1"), computer), "", "", 0, core.ErrUnauthorized},
	} {
		color, failure := actingColor(tc.view, tc.requested, core.Actor{UserID: tc.user})
		switch {
		case tc.code != "" && (failure == nil || failure.Error.Code != tc.code):
			t.Errorf("%s: got %v %+v, want error %s", name, color, failure, tc.code)
		case tc.code == "" && (failure != nil || color != tc.want):
			t.Errorf("%s: got %v %+v, want %v", name, color, failure, tc.want)
		}
	}
}

// benchLine is twenty legal plies from the start position, so every move of
// the benchmarks is accepted.
var benchLine = []string{
	"e2e4", "e7e5", "g1f3", "b8c6", "f1b5", "a7a6", "b5a4", "g8f6", "e1g1", "f8e7",
	"f1e1", "b7b5", "a4b3", "d7d6", "c2c3", "e8g8", "h2h3", "c6b8", "d2d4", "b8d7",
}

func benchProcessor(b *testing.B) *Processor {
	b.Helper()
	if _, err := exec.LookPath("stockfish"); err != nil {
		b.Skip("stockfish is not in PATH")
	}
	svc, err := service.New(nil, []byte("test-secret-test-secret-test-secret"))
	if err != nil {
		b.Fatal(err)
	}
	p, err := New(svc)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { p.Close() })
	return p
}

func benchGame(b *testing.B, p *Processor) string {
	human := core.PlayerConfig{Type: core.PlayerHuman}
	resp := p.Execute(b.Context(), NewCreateGameCommand(core.CreateGameRequest{White: human, Black: human}))
	if !resp.Success {
		b.Fatal(resp.Error.Error)
	}
	return resp.Data.(core.GameResponse).GameID
}

// BenchmarkHumanMove is one accepted human move: its latency alone, and the
// throughput of games moving at once.
func BenchmarkHumanMove(b *testing.B) {
	play := func(b *testing.B, p *Processor, next func() bool) {
		for ply, gameID := len(benchLine), ""; next(); ply++ {
			if ply == len(benchLine) {
				b.StopTimer()
				ply, gameID = 0, benchGame(b, p)
				b.StartTimer()
			}
			resp := p.Execute(b.Context(), NewMakeMoveCommand(gameID, core.MoveRequest{Move: benchLine[ply]}))
			if !resp.Success {
				b.Fatal(resp.Error.Error)
			}
		}
	}
	b.Run("one game", func(b *testing.B) {
		p, i := benchProcessor(b), 0
		b.ResetTimer()
		play(b, p, func() bool { i++; return i <= b.N })
	})
	b.Run("parallel games", func(b *testing.B) {
		p := benchProcessor(b)
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) { play(b, p, pb.Next) })
	})
}

func BenchmarkCreateGame(b *testing.B) {
	p := benchProcessor(b)
	b.ResetTimer()
	for b.Loop() {
		benchGame(b, p)
	}
}
