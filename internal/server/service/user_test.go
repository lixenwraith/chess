package service

import (
	"testing"

	"github.com/lixenwraith/auth"
)

func TestValidateTokenRequiresSessionBoundToSubject(t *testing.T) {
	svc := newPersistentTestService(t)
	user, sessionID, err := svc.RegisterUser("alice", "", "Password1", false)
	if err != nil {
		t.Fatal(err)
	}

	validToken, err := svc.GenerateUserToken(user.UserID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if gotUserID, _, err := svc.ValidateToken(validToken); err != nil || gotUserID != user.UserID {
		t.Fatalf("valid token rejected: user=%q err=%v", gotUserID, err)
	}

	missingSession, err := auth.GenerateHS256Token(svc.jwtSecret, user.UserID, nil, SessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ValidateToken(missingSession); err == nil {
		t.Fatal("token without a persisted session was accepted")
	}

	wrongSubject, err := auth.GenerateHS256Token(svc.jwtSecret, "other-user", map[string]any{
		"session_id": sessionID,
	}, SessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ValidateToken(wrongSubject); err == nil {
		t.Fatal("session was accepted for a different JWT subject")
	}

	if err := svc.InvalidateSession(sessionID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ValidateToken(validToken); err == nil {
		t.Fatal("invalidated session was accepted")
	}
}
