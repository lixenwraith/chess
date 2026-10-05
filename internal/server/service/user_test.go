package service

import (
	"errors"
	"testing"
	"time"

	"github.com/lixenwraith/chess/internal/server/chess"
	"github.com/lixenwraith/chess/internal/server/core"
	"github.com/lixenwraith/chess/internal/server/storage"

	"github.com/google/uuid"
	"github.com/lixenwraith/auth"
)

var testJWTSecret = []byte("test-secret-test-secret-test-secret")

func TestValidateTokenRequiresScopedTokenBoundToSession(t *testing.T) {
	svc := newPersistentTestService(t)
	user, sessionID, err := svc.RegisterUser("alice", "", "Password1")
	if err != nil {
		t.Fatal(err)
	}

	validToken, err := svc.GenerateUserToken(user.UserID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	gotUserID, claims, err := svc.ValidateToken(validToken)
	if err != nil || gotUserID != user.UserID || claims["session_id"] != sessionID {
		t.Fatalf("valid token rejected: user=%q claims=%v err=%v", gotUserID, claims, err)
	}

	scoped, err := auth.NewJWT(testJWTSecret,
		auth.WithIssuer(JWTIssuer), auth.WithAudience([]string{JWTAudience}))
	if err != nil {
		t.Fatal(err)
	}
	unscoped, err := auth.NewJWT(testJWTSecret)
	if err != nil {
		t.Fatal(err)
	}
	foreignAudience, err := auth.NewJWT(testJWTSecret,
		auth.WithIssuer(JWTIssuer), auth.WithAudience([]string{"other-service"}))
	if err != nil {
		t.Fatal(err)
	}
	for name, mint := range map[string]func() (string, error){
		"missing session": func() (string, error) { return scoped.GenerateToken(user.UserID, nil) },
		"wrong subject": func() (string, error) {
			return scoped.GenerateToken(uuid.NewString(), map[string]any{"session_id": sessionID})
		},
		"malformed subject": func() (string, error) {
			return scoped.GenerateToken("other-user", map[string]any{"session_id": sessionID})
		},
		"no issuer or audience": func() (string, error) {
			return unscoped.GenerateToken(user.UserID, map[string]any{"session_id": sessionID})
		},
		"foreign audience": func() (string, error) {
			return foreignAudience.GenerateToken(user.UserID, map[string]any{"session_id": sessionID})
		},
	} {
		token, err := mint()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := svc.ValidateToken(token); err == nil {
			t.Errorf("%s: token accepted", name)
		}
	}

	if err := svc.InvalidateSession(sessionID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ValidateToken(validToken); err == nil {
		t.Fatal("invalidated session was accepted")
	}
}

func TestAuthenticateUserFailuresAreIndistinguishable(t *testing.T) {
	svc := newPersistentTestService(t)
	if _, _, err := svc.RegisterUser("alice", "alice@example.com", "Password1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RegisterUser("ALICE", "", "Password1"); !errors.Is(err, ErrUserExists) {
		t.Fatalf("duplicate registration = %v, want ErrUserExists", err)
	}

	user, sessionID, err := svc.AuthenticateUser("alice@example.com", "Password1")
	if err != nil || sessionID == "" || user.Username != "alice" {
		t.Fatalf("login by email = %+v, %q, %v", user, sessionID, err)
	}

	for name, attempt := range map[string][2]string{
		"unknown user":   {"nobody", "Password1"},
		"unknown email":  {"nobody@example.com", "Password1"},
		"wrong password": {"alice", "Password2"},
	} {
		if _, _, err := svc.AuthenticateUser(attempt[0], attempt[1]); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: error = %v, want ErrInvalidCredentials", name, err)
		}
	}
}

func TestRegistrationCapIsConfigurable(t *testing.T) {
	svc := newPersistentTestService(t)
	svc.SetMaxUsers(1)
	if _, _, err := svc.RegisterUser("alice", "", "Password1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RegisterUser("bob", "", "Password1"); !errors.Is(err, ErrAtCapacity) {
		t.Fatalf("registration past the cap = %v, want ErrAtCapacity", err)
	}
	svc.SetMaxUsers(0)
	if _, _, err := svc.RegisterUser("bob", "", "Password1"); err != nil {
		t.Fatalf("registration without a cap: %v", err)
	}
}

func TestPasswordHashingIsBounded(t *testing.T) {
	svc, err := New(nil, testJWTSecret)
	if err != nil {
		t.Fatal(err)
	}
	var releases []func()
	for range MaxConcurrentKDF {
		release, err := svc.acquireKDF()
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	started := time.Now()
	if _, err := svc.hashPassword("Password1"); !errors.Is(err, ErrAuthBusy) {
		t.Fatalf("saturated KDF = %v, want ErrAuthBusy", err)
	}
	if waited := time.Since(started); waited < kdfWaitTimeout {
		t.Fatalf("gave up after %v, want %v", waited, kdfWaitTimeout)
	}
	releases[0]()
	if _, err := svc.hashPassword("Password1"); err != nil {
		t.Fatalf("hash after release: %v", err)
	}
}

func TestNewRejectsShortJWTSecret(t *testing.T) {
	if _, err := New((*storage.Store)(nil), make([]byte, 31)); err == nil {
		t.Fatal("31-byte JWT secret accepted")
	}
}

func TestDeleteAccountReleasesLiveClaims(t *testing.T) {
	svc := newPersistentTestService(t)
	user, sessionID, err := svc.RegisterUser("alice", "", "Password1")
	if err != nil {
		t.Fatal(err)
	}
	token, err := svc.GenerateUserToken(user.UserID, sessionID)
	if err != nil {
		t.Fatal(err)
	}

	gameID := uuid.NewString()
	white := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorWhite)
	white.ID, white.ClaimedBy = user.UserID, user.UserID
	black := core.NewPlayer(core.PlayerConfig{Type: core.PlayerComputer, Level: 1, SearchTime: 100}, core.ColorBlack)
	if err := svc.CreateGame(gameID, white, black, chess.StartFEN, core.ColorWhite, core.StateOngoing, core.TermNone); err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteAccount(user.UserID, sessionID, "Password2"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: %v, want ErrInvalidCredentials", err)
	}
	if _, _, err := svc.ValidateToken(token); err != nil {
		t.Fatalf("a refused deletion ended the session: %v", err)
	}

	if err := svc.DeleteAccount(user.UserID, sessionID, "Password1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ValidateToken(token); err == nil {
		t.Error("token still valid after deletion")
	}
	view, err := svc.GetGameView(gameID)
	if err != nil {
		t.Fatal(err)
	}
	if owner := view.WhitePlayer.ClaimedBy; owner != "" {
		t.Errorf("live game still claimed by the deleted account: %q", owner)
	}
}
