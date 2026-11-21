package engine

import "testing"

// The budget ran out in depth 15: its first line is searched, its second only
// bounded, and its third still depth 14's.
func TestAnalysisKeepsTheDeepestIterationThatFinishedEveryLine(t *testing.T) {
	search := iteration{lines: 3}
	for _, report := range []string{
		"info depth 14 seldepth 18 multipv 1 score cp 38 nodes 400000 pv f3e5 e7d6",
		"info depth 14 seldepth 20 multipv 2 score cp 35 nodes 400000 pv h2h4 e7d6",
		"info depth 14 seldepth 18 multipv 3 score mate -4 nodes 400000 pv h2h3 e7d6",
		"info depth 15 currmove f3e5 currmovenumber 1",
		"info depth 15 seldepth 20 multipv 1 score cp 41 nodes 500395 pv h2h4 e7d6 f3e5",
		"info depth 15 seldepth 21 multipv 2 score cp 31 upperbound nodes 500395 pv f3e5 f6d7",
		"info depth 14 seldepth 18 multipv 3 score mate -4 nodes 500395 pv h2h3 e7d6",
		"bestmove h2h4 ponder e7d6",
	} {
		search.read(report)
	}
	want := []Line{
		{Depth: 14, Score: 38, Moves: []string{"f3e5", "e7d6"}},
		{Depth: 14, Score: 35, Moves: []string{"h2h4", "e7d6"}},
		{Depth: 14, Mate: -4, Moves: []string{"h2h3", "e7d6"}},
	}
	if len(search.done) != len(want) {
		t.Fatalf("lines = %+v, want %+v", search.done, want)
	}
	for i, line := range search.done {
		if line.Depth != want[i].Depth || line.Score != want[i].Score || line.Mate != want[i].Mate ||
			len(line.Moves) != 2 || line.Moves[0] != want[i].Moves[0] {
			t.Errorf("line %d = %+v, want %+v", i+1, line, want[i])
		}
	}
}
