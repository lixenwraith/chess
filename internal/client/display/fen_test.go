package display

import "testing"

func TestBoardFromFEN(t *testing.T) {
	got, err := BoardFromFEN("rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1")
	if err != nil {
		t.Fatal(err)
	}
	want := "  a b c d e f g h\n" +
		"8 r n b q k b n r 8\n" +
		"7 p p p p p p p p 7\n" +
		"6 . . . . . . . . 6\n" +
		"5 . . . . . . . . 5\n" +
		"4 . . . . P . . . 4\n" +
		"3 . . . . . . . . 3\n" +
		"2 P P P P . P P P 2\n" +
		"1 R N B Q K B N R 1\n" +
		"  a b c d e f g h"
	if got != want {
		t.Errorf("board:\n%s\nwant:\n%s", got, want)
	}
	for _, bad := range []string{"", "8/8/8/8/8/8/8 w - - 0 1", "9/8/8/8/8/8/8/8 w - - 0 1", "x7/8/8/8/8/8/8/8 w - - 0 1"} {
		if _, err := BoardFromFEN(bad); err == nil {
			t.Errorf("BoardFromFEN(%q) accepted", bad)
		}
	}
}
