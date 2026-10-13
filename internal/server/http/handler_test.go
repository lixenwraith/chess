package http

import (
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lixenwraith/chess/internal/server/chess"
	"github.com/lixenwraith/chess/internal/server/core"
	"github.com/lixenwraith/chess/internal/server/service"
	"github.com/lixenwraith/chess/internal/server/storage"
	"github.com/lixenwraith/chess/internal/server/storage/pgtest"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

func TestClientIPTrustsProxyHeaderOnlyFromTrustedPeers(t *testing.T) {
	// app.Test connections originate from 0.0.0.0.
	for name, test := range map[string]struct {
		opts Options
		want string
	}{
		"no proxy configured":  {Options{ProxyHeader: "X-Real-IP"}, "0.0.0.0"},
		"untrusted peer":       {Options{TrustedProxies: []string{"10.0.0.1"}, ProxyHeader: "X-Real-IP"}, "0.0.0.0"},
		"trusted peer":         {Options{TrustedProxies: []string{"0.0.0.0/32"}, ProxyHeader: "X-Real-IP"}, "203.0.113.9"},
		"trusted, header only": {Options{TrustedProxies: []string{"0.0.0.0"}, ProxyHeader: "X-Real-IP"}, "203.0.113.9"},
	} {
		app := fiber.New(appConfig(test.opts))
		app.Get("/ip", func(c *fiber.Ctx) error { return c.SendString(clientIP(c)) })
		request := httptest.NewRequest("GET", "/ip", nil)
		request.Header.Set("X-Real-IP", "203.0.113.9")
		request.Header.Set("X-Forwarded-For", "198.51.100.1") // never consulted
		response, err := app.Test(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		if string(body) != test.want {
			t.Errorf("%s: client IP = %q, want %q", name, body, test.want)
		}
	}

	// A trusted proxy that omits the header must not collapse clients into one key.
	app := fiber.New(appConfig(Options{TrustedProxies: []string{"0.0.0.0"}, ProxyHeader: "X-Real-IP"}))
	app.Get("/ip", func(c *fiber.Ctx) error { return c.SendString(clientIP(c)) })
	response, err := app.Test(httptest.NewRequest("GET", "/ip", nil))
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := io.ReadAll(response.Body); string(body) != "0.0.0.0" {
		t.Errorf("missing proxy header: client IP = %q, want peer address", body)
	}
}

func TestIsValidUUIDRequiresCanonicalForm(t *testing.T) {
	for value, want := range map[string]bool{
		"5579b47e-4d3b-4eb3-abbd-846fe94cb955":          true,
		"{5579b47e-4d3b-4eb3-abbd-846fe94cb955}":        false,
		"urn:uuid:5579b47e-4d3b-4eb3-abbd-846fe94cb955": false,
		"5579b47e4d3b4eb3abbd846fe94cb955":              false,
		"not-a-uuid":                                    false,
	} {
		if got := isValidUUID(value); got != want {
			t.Errorf("isValidUUID(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestAPIRoutes(t *testing.T) {
	svc, err := service.New(nil, []byte("test-secret-test-secret-test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	// Route registration only; no handler runs, so no processor is needed.
	app := NewFiberApp(nil, svc, Options{DevMode: true})

	registered := make(map[string]bool)
	for _, route := range app.GetRoutes(true) {
		registered[route.Method+" "+route.Path] = true
	}
	for _, want := range []string{
		"GET /health",
		"POST /api/auth/register",
		"POST /api/auth/login",
		"GET /api/auth/me",
		"POST /api/auth/logout",
		"DELETE /api/auth/me",
		"POST /api/games",
		"GET /api/games/:gameId",
		"GET /api/games/:gameId/history",
		"GET /api/games/:gameId/pgn",
		"POST /api/games/:gameId/moves",
		"POST /api/games/:gameId/resign",
		"POST /api/games/:gameId/draw",
		"GET /api/users/me/games",
	} {
		if !registered[want] {
			t.Errorf("route %s is not registered", want)
		}
	}
}

func TestETagMatches(t *testing.T) {
	const etag = `"0123abcd"`
	for header, want := range map[string]bool{
		"":                      false,
		`"0123abcd"`:            true,
		`W/"0123abcd"`:          true,
		`"other", "0123abcd"`:   true,
		`"other",W/"0123abcd" `: true,
		"*":                     true,
		`"0123abc"`:             false,
		`0123abcd`:              false,
	} {
		if got := etagMatches(header, etag); got != want {
			t.Errorf("etagMatches(%q) = %v, want %v", header, got, want)
		}
	}
}

func TestStoredGameEndpoints(t *testing.T) {
	store, err := storage.NewStore(pgtest.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InitDB(); err != nil {
		t.Fatal(err)
	}
	svc, err := service.New(store, []byte("test-secret-test-secret-test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Shutdown(time.Second) })

	gameID := uuid.NewString()
	white := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorWhite)
	black := core.NewPlayer(core.PlayerConfig{Type: core.PlayerHuman}, core.ColorBlack)
	if err := svc.CreateGame(gameID, white, black, chess.StartFEN, core.ColorWhite, core.StateOngoing, core.TermNone); err != nil {
		t.Fatal(err)
	}
	pos, _ := chess.ParseFEN(chess.StartFEN)
	for i, uci := range []string{"e2e4", "e7e5"} {
		m, err := pos.ParseUCI(uci)
		if err != nil {
			t.Fatal(err)
		}
		next := pos.Play(m)
		turn := core.ColorWhite
		if i == 1 {
			turn = core.ColorBlack
		}
		if err := svc.ApplyMoveWithState(gameID, service.MoveCommit{
			ExpectedFEN: pos.FEN(), ExpectedState: core.StateOngoing, ExpectedTurn: turn,
			MoveUCI: uci, NewFEN: next.FEN(), State: core.StateOngoing,
		}); err != nil {
			t.Fatal(err)
		}
		pos = next
	}

	app := NewFiberApp(nil, svc, Options{DevMode: true})
	get := func(path string, header ...string) (*nethttp.Response, string) {
		t.Helper()
		request := httptest.NewRequest("GET", path, nil)
		for i := 0; i+1 < len(header); i += 2 {
			request.Header.Set(header[i], header[i+1])
		}
		response, err := app.Test(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		return response, string(body)
	}

	response, body := get("/api/games/" + gameID + "/history")
	etag := response.Header.Get("ETag")
	if response.StatusCode != 200 || etag == "" || response.Header.Get("Cache-Control") != "private, no-cache" ||
		!strings.Contains(body, `"san":"e4"`) || !strings.Contains(body, `"pgnResult":"*"`) ||
		!strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("history: %d %v %s", response.StatusCode, response.Header, body)
	}
	if response, body = get("/api/games/"+gameID+"/history", "If-None-Match", etag); response.StatusCode != 304 || body != "" {
		t.Fatalf("revalidated history: %d %q", response.StatusCode, body)
	}

	response, body = get("/api/games/" + gameID + "/pgn")
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "application/x-chess-pgn; charset=utf-8" ||
		!strings.HasPrefix(response.Header.Get("Content-Disposition"), `attachment; filename="chess-`) ||
		!strings.HasSuffix(body, "\n\n1. e4 e5 *\n") {
		t.Fatalf("pgn: %d %v\n%s", response.StatusCode, response.Header, body)
	}
	if response, body = get("/api/games/" + gameID + "/pgn?ply=1"); response.StatusCode != 200 ||
		!strings.HasSuffix(body, "\n\n1. e4 *\n") ||
		!strings.Contains(response.Header.Get("Content-Disposition"), "-ply1.pgn") {
		t.Fatalf("pgn ply 1: %d %v\n%s", response.StatusCode, response.Header, body)
	}

	for path, want := range map[string]int{
		"/api/games/" + gameID + "/pgn?ply=3":         400,
		"/api/games/" + gameID + "/pgn?ply=-1":        400,
		"/api/games/" + gameID + "/pgn?ply=x":         400,
		"/api/games/" + uuid.NewString() + "/pgn":     404,
		"/api/games/" + uuid.NewString() + "/history": 404,
		"/api/games/not-a-uuid/pgn":                   400,
	} {
		if response, body := get(path); response.StatusCode != want {
			t.Errorf("GET %s = %d %s, want %d", path, response.StatusCode, body, want)
		}
	}
}

func TestDeleteAccountRequiresPasswordAndEndsTheSession(t *testing.T) {
	store, err := storage.NewStore(pgtest.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InitDB(); err != nil {
		t.Fatal(err)
	}
	svc, err := service.New(store, []byte("test-secret-test-secret-test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Shutdown(time.Second) })
	app := NewFiberApp(nil, svc, Options{DevMode: true})

	send := func(method, path, token, body string) int {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		request := httptest.NewRequest(method, path, reader)
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := app.Test(request, 10_000)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode
	}

	user, sessionID, err := svc.RegisterUser("alice", "", "Password1")
	if err != nil {
		t.Fatal(err)
	}
	token, err := svc.GenerateUserToken(user.UserID, sessionID)
	if err != nil {
		t.Fatal(err)
	}

	for name, test := range map[string]struct {
		token, body string
		want        int
	}{
		"no token":       {"", `{"password":"Password1"}`, 401},
		"no password":    {token, `{}`, 400},
		"wrong password": {token, `{"password":"Password2"}`, 401},
	} {
		if got := send("DELETE", "/api/auth/me", test.token, test.body); got != test.want {
			t.Errorf("%s: status %d, want %d", name, got, test.want)
		}
	}
	if got := send("GET", "/api/auth/me", token, ""); got != 200 {
		t.Fatalf("a refused deletion must leave the account working: /auth/me = %d", got)
	}

	if got := send("DELETE", "/api/auth/me", token, `{"password":"Password1"}`); got != 204 {
		t.Fatalf("delete: status %d, want 204", got)
	}
	if got := send("GET", "/api/auth/me", token, ""); got != 401 {
		t.Errorf("token still accepted after deletion: /auth/me = %d", got)
	}
	if _, _, err := svc.AuthenticateUser("alice", "Password1"); err == nil {
		t.Error("deleted account can still sign in")
	}
}
