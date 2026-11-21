package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lixenwraith/chess/internal/client/display"
)

const HttpTimeout = 30 * time.Second

type Client struct {
	BaseURL    string
	AuthToken  string
	HTTPClient *http.Client
	Verbose    bool
}

func New(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{
			Timeout: HttpTimeout,
		},
	}
}

func (c *Client) SetVerbose(v bool) {
	c.Verbose = v
}

func (c *Client) SetBaseURL(url string) {
	c.BaseURL = strings.TrimRight(url, "/")
}

func (c *Client) SetToken(token string) {
	c.AuthToken = token
}

func (c *Client) doRequest(method, path string, body any, result any) error {
	return c.do(context.Background(), method, path, body, result)
}

// do sends one request under ctx; a cancelled ctx returns its error unprinted.
func (c *Client) do(ctx context.Context, method, path string, body any, result any) error {
	url := c.BaseURL + path

	var bodyReader io.Reader
	var bodyStr string
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(jsonData)
		bodyStr = string(jsonData)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return err
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AuthToken)
	}

	display.Print(display.Blue, "\n[API] %s %s\n", method, path)
	if bodyStr != "" {
		if c.Verbose {
			var prettyBody any
			json.Unmarshal([]byte(bodyStr), &prettyBody)
			prettyJSON, _ := json.MarshalIndent(prettyBody, "", "  ")
			display.Println(display.Cyan, "Request Body:")
			display.Println(display.Reset, "%s", string(prettyJSON))
		} else {
			display.Print(display.Blue, "%s\n", bodyStr)
		}
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		display.Print(display.Red, "[ERROR] %s\n", err.Error())
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	statusColor := display.Green
	if resp.StatusCode >= 400 {
		statusColor = display.Red
	}
	display.Printf("%s[%d %s]%s\n", statusColor, resp.StatusCode, http.StatusText(resp.StatusCode), display.Reset)

	if c.Verbose && len(respBody) > 0 {
		var prettyResp any
		if err := json.Unmarshal(respBody, &prettyResp); err == nil {
			prettyJSON, _ := json.MarshalIndent(prettyResp, "", "  ")
			display.Println(display.Cyan, "Response Body:")
			display.Println(display.Reset, "%s", string(prettyJSON))
		} else {
			display.Println(display.Cyan, "Response:")
			display.Println(display.Reset, "%s", string(respBody))
		}
	}

	if resp.StatusCode >= 400 {
		var errResp ErrorResponse
		if err := json.Unmarshal(respBody, &errResp); err == nil {
			if !c.Verbose {
				display.Print(display.Red, "Error: %s\n", errResp.Error)
				if errResp.Code != "" {
					display.Print(display.Red, "Code: %s\n", errResp.Code)
				}
				if errResp.Details != "" {
					display.Print(display.Red, "Details: %s\n", errResp.Details)
				}
			}
		} else if !c.Verbose {
			display.Println(display.Red, "%s", string(respBody))
		}
		return fmt.Errorf("request failed with status %d", resp.StatusCode)
	}

	// A *string result receives a non-JSON body as-is
	if text, ok := result.(*string); ok {
		*text = string(respBody)
		return nil
	}
	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			display.Print(display.Red, "Response parse error: %s\n", err.Error())
			display.Print(display.Green, "Raw response: %s\n", string(respBody))
			return err
		}
	}

	return nil
}

func (c *Client) Health() (*HealthResponse, error) {
	var resp HealthResponse
	err := c.doRequest("GET", "/health", nil, &resp)
	return &resp, err
}

func (c *Client) CreateGame(req *CreateGameRequest) (*GameResponse, error) {
	var resp GameResponse
	err := c.doRequest("POST", "/api/games", req, &resp)
	return &resp, err
}

func (c *Client) GetGame(gameID string) (*GameResponse, error) {
	var resp GameResponse
	err := c.doRequest("GET", "/api/games/"+gameID, nil, &resp)
	return &resp, err
}

// GetGameWithPoll long-polls the game; cancelling ctx ends the wait.
func (c *Client) GetGameWithPoll(ctx context.Context, gameID string, moveCount int) (*GameResponse, error) {
	var resp GameResponse
	path := fmt.Sprintf("/api/games/%s?wait=true&moveCount=%d", gameID, moveCount)
	err := c.do(ctx, "GET", path, nil, &resp)
	return &resp, err
}

func (c *Client) DeleteGame(gameID string) error {
	return c.doRequest("DELETE", "/api/games/"+gameID, nil, nil)
}

func (c *Client) MakeMove(gameID string, move string) (*GameResponse, error) {
	req := &MoveRequest{Move: move}
	var resp GameResponse
	err := c.doRequest("POST", "/api/games/"+gameID+"/moves", req, &resp)
	return &resp, err
}

func (c *Client) UndoMoves(gameID string, count int) (*GameResponse, error) {
	req := &UndoRequest{Count: count}
	var resp GameResponse
	err := c.doRequest("POST", "/api/games/"+gameID+"/undo", req, &resp)
	return &resp, err
}

// Resign ends the game in the opponent's favor; color may be "" when the
// caller plays exactly one human side.
func (c *Client) Resign(gameID, color string) (*GameResponse, error) {
	var resp GameResponse
	err := c.doRequest("POST", "/api/games/"+gameID+"/resign", &ResignRequest{Color: color}, &resp)
	return &resp, err
}

// Draw offers, accepts, or declines a draw; the response's DrawOutcome says
// what happened.
func (c *Client) Draw(gameID, action, color string) (*GameResponse, error) {
	var resp GameResponse
	err := c.doRequest("POST", "/api/games/"+gameID+"/draw", &DrawRequest{Action: action, Color: color}, &resp)
	return &resp, err
}

func (c *Client) GetBoard(gameID string) (*BoardResponse, error) {
	var resp BoardResponse
	err := c.doRequest("GET", "/api/games/"+gameID+"/board", nil, &resp)
	return &resp, err
}

func (c *Client) GetGameHistory(gameID string) (*GameHistoryResponse, error) {
	var resp GameHistoryResponse
	err := c.doRequest("GET", "/api/games/"+gameID+"/history", nil, &resp)
	return &resp, err
}

// GetGamePGN returns the stored game in PGN; ply < 0 exports every move.
func (c *Client) GetGamePGN(gameID string, ply int) (string, error) {
	path := "/api/games/" + gameID + "/pgn"
	if ply >= 0 {
		path += fmt.Sprintf("?ply=%d", ply)
	}
	var text string
	err := c.doRequest("GET", path, nil, &text)
	return text, err
}

// GetMyGames lists the caller's games, newest first; cursor is "" for the
// first page, then the previous page's NextCursor.
func (c *Client) GetMyGames(limit int, cursor string) (*GameListResponse, error) {
	var resp GameListResponse
	path := fmt.Sprintf("/api/users/me/games?limit=%d", limit)
	if cursor != "" {
		path += "&cursor=" + url.QueryEscape(cursor)
	}
	err := c.doRequest("GET", path, nil, &resp)
	return &resp, err
}

// RemoveMyGame takes a stored game off the caller's list: deleted when the
// caller alone holds it, released when another player keeps it.
func (c *Client) RemoveMyGame(gameID string) (*RemoveGameResponse, error) {
	var resp RemoveGameResponse
	err := c.doRequest("DELETE", "/api/users/me/games/"+gameID, nil, &resp)
	return &resp, err
}

// ImportGame stores a PGN game for the caller.
func (c *Client) ImportGame(pgn string) (*ImportGameResponse, error) {
	var resp ImportGameResponse
	err := c.doRequest("POST", "/api/games/import", &ImportGameRequest{PGN: pgn}, &resp)
	return &resp, err
}

func (c *Client) Register(username, password, email string) (*AuthResponse, error) {
	req := &RegisterRequest{
		Username: username,
		Password: password,
		Email:    email,
	}
	var resp AuthResponse
	err := c.doRequest("POST", "/api/auth/register", req, &resp)
	return &resp, err
}

func (c *Client) Login(identifier, password string) (*AuthResponse, error) {
	req := &LoginRequest{
		Identifier: identifier,
		Password:   password,
	}
	var resp AuthResponse
	err := c.doRequest("POST", "/api/auth/login", req, &resp)
	return &resp, err
}

func (c *Client) Logout() error {
	return c.doRequest("POST", "/api/auth/logout", nil, nil)
}

func (c *Client) GetCurrentUser() (*UserResponse, error) {
	var resp UserResponse
	err := c.doRequest("GET", "/api/auth/me", nil, &resp)
	return &resp, err
}

func (c *Client) RawRequest(method, path string, body string) error {
	var bodyData any
	if body != "" {
		if err := json.Unmarshal([]byte(body), &bodyData); err != nil {
			bodyData = body
		}
	}

	return c.doRequest(method, path, bodyData, nil)
}
