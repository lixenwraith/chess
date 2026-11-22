package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/lixenwraith/chess/internal/server/storage/pgtest"
)

// TestResetMailGoesOnceThenOnceMoreThenNotForAWindow: an address gets a mail
// at once, a second after the spacing, and no third until a window after the
// second; a mail that was not sent is given back; all addresses share a cap.
func TestResetMailGoesOnceThenOnceMoreThenNotForAWindow(t *testing.T) {
	store := openStore(t, pgtest.DSN(t))
	ctx := t.Context()
	limits := ResetMailLimits{Window: 24 * time.Hour, Spacing: time.Minute, Total: 4}
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	address := func(b byte) []byte { return append(make([]byte, 31), b) }

	for _, step := range []struct {
		after time.Duration
		want  error
	}{
		{0, nil},
		{30 * time.Second, ErrResetMailHeld},
		{time.Minute, nil},
		{2 * time.Hour, ErrResetMailHeld},
		{24*time.Hour + 30*time.Second, ErrResetMailHeld}, // a day after the first, not the second
		{24*time.Hour + time.Minute, nil},
		{24*time.Hour + 2*time.Minute, nil},
		{47 * time.Hour, ErrResetMailHeld},
	} {
		if err := store.ReserveResetMail(ctx, address(1), start.Add(step.after), limits); !errors.Is(err, step.want) {
			t.Errorf("after %v: %v, want %v", step.after, err, step.want)
		}
	}

	later := start.Add(72 * time.Hour)
	if err := store.ReserveResetMail(ctx, address(2), later, limits); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseResetMail(ctx, address(2), later); err != nil {
		t.Fatal(err)
	}
	for b := byte(2); b <= 5; b++ {
		if err := store.ReserveResetMail(ctx, address(b), later, limits); err != nil {
			t.Fatalf("address %d within the cap: %v", b, err)
		}
	}
	if err := store.ReserveResetMail(ctx, address(6), later, limits); !errors.Is(err, ErrResetMailCapped) {
		t.Errorf("fifth address in a window of four: %v", err)
	}
	if err := store.ReserveResetMail(ctx, address(5), later, limits); !errors.Is(err, ErrResetMailHeld) {
		t.Errorf("an address held and capped: %v", err)
	}

	if err := store.DeleteResetMailsBefore(ctx, later.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveResetMail(ctx, address(6), later.Add(time.Second), limits); err != nil {
		t.Errorf("after the old mail is forgotten: %v", err)
	}
}
