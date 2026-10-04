package service

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/lixenwraith/chess/internal/server/chess"
	"github.com/lixenwraith/chess/internal/server/storage"

	"github.com/google/uuid"
	"github.com/lixenwraith/auth"
)

func TestIntegritySweep(t *testing.T) {
	svc, dsn := newPersistentTestServiceDSN(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	stored := func(gameID string) bool {
		t.Helper()
		var exists bool
		if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM games WHERE game_id = $1)`, gameID).
			Scan(&exists); err != nil {
			t.Fatal(err)
		}
		return exists
	}
	// played creates a game, plays the moves, and unloads it from memory.
	played := func(uci ...string) string {
		t.Helper()
		gameID := newHumanGame(t, svc)
		for _, u := range uci {
			move(t, svc, gameID, "", u)
		}
		if err := svc.DeleteGame(gameID); err != nil {
			t.Fatal(err)
		}
		return gameID
	}

	valid := played("e2e4", "e7e5", "g1f3")
	gap := played("e2e4", "e7e5", "g1f3")
	corrupt := played("e2e4", "e7e5")
	loaded := newHumanGame(t, svc)
	vanished := newHumanGame(t, svc)

	old := time.Now().Add(-48 * time.Hour)
	orphan, claimed := uuid.NewString(), uuid.NewString()
	aliceHash, err := auth.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	alice, mallory := uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO users (user_id, username, password_hash) VALUES ($1, 'alice', $2), ($3, 'mallory', 'hunter2')`,
		alice, aliceHash, mallory)
	for gameID, claimant := range map[string]string{orphan: uuid.NewString(), claimed: alice} {
		if err := svc.store.RecordNewGame(storage.GameRecord{
			GameID: gameID, InitialFEN: chess.StartFEN,
			WhitePlayerID: claimant, WhiteType: 1, WhiteClaimedBy: claimant,
			BlackPlayerID: uuid.NewString(), BlackType: 1, StartTimeUTC: old,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.GetGameHistory(valid); err != nil { // flush queued writes
		t.Fatal(err)
	}

	// Damage made by hand, as with psql.
	exec(`DELETE FROM moves WHERE game_id = $1 AND move_number = 2`, gap)
	exec(`UPDATE moves SET fen_after_move = $2 WHERE game_id = $1 AND move_number = 2`, corrupt, chess.StartFEN)
	exec(`DELETE FROM games WHERE game_id = $1`, vanished)

	svc.SetIntegrityMode(IntegrityReport)
	svc.checkIntegrity(time.Now())
	for _, gameID := range []string{valid, gap, corrupt, orphan, claimed, loaded} {
		if !stored(gameID) {
			t.Fatalf("report mode deleted game %s", gameID)
		}
	}
	if _, err := svc.GetGameView(vanished); err != nil {
		t.Fatal("report mode unloaded a game")
	}

	svc.SetIntegrityMode(IntegrityDelete)
	svc.checkIntegrity(time.Now())
	if _, err := svc.GetGameHistory(valid); err != nil { // flush the deletion
		t.Fatal(err)
	}
	for gameID, want := range map[string]bool{
		valid: true, loaded: true, claimed: true, gap: false, corrupt: false, orphan: false,
	} {
		if got := stored(gameID); got != want {
			t.Errorf("game %s stored = %v, want %v", gameID, got, want)
		}
	}
	if _, err := svc.GetGameView(vanished); !errors.Is(err, ErrGameNotFound) {
		t.Errorf("game with a deleted row still loaded: %v", err)
	}
	if _, err := svc.GetGameView(loaded); err != nil {
		t.Errorf("healthy loaded game was unloaded: %v", err)
	}
	var users int
	if err := db.QueryRow(`SELECT count(*) FROM users WHERE user_id IN ($1, $2)`, alice, mallory).
		Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 1 {
		t.Errorf("accounts left = %d, want alice only", users)
	}
	if health := svc.GetStorageHealth(); health != "ok" {
		t.Errorf("storage health %q after the sweep", health)
	}

	// A healthy database gives a clean second run.
	findings, err := svc.findIntegrityProblems(time.Now(), map[string]bool{loaded: true}, AnonymousGameTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings.games)+len(findings.users)+len(findings.vanished) != 0 {
		t.Errorf("second sweep found %+v", findings)
	}
}

func TestParseIntegrityMode(t *testing.T) {
	for in, want := range map[string]IntegrityMode{"": IntegrityOff, "off": IntegrityOff,
		"report": IntegrityReport, "delete": IntegrityDelete} {
		if got, err := ParseIntegrityMode(in); err != nil || got != want {
			t.Errorf("ParseIntegrityMode(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseIntegrityMode("purge"); err == nil {
		t.Error("ParseIntegrityMode accepted an unknown mode")
	}
}
