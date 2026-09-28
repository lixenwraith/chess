package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"chess/internal/client/display"
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

// SetBaseURL updates the API base URL for the client
func (c *Client) SetBaseURL(url string) {
	c.BaseURL = strings.TrimRight(url, "/")
}

func (c *Client) SetToken(token string) {
	c.AuthToken = token
}

func (c *Client) doRequest(method, path string, body any, result any) error {
	url := c.BaseURL + path

	// Prepare body
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

	// Create request
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return err
	}

	// Set headers
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AuthToken)
	}

	// Display request
	display.Print(display.Blue, "\n[API] %s %s\n", method, path)
	if bodyStr != "" {
		if c.Verbose {
			// Display request body if verbose
			var prettyBody any
			json.Unmarshal([]byte(bodyStr), &prettyBody)
			prettyJSON, _ := json.MarshalIndent(prettyBody, "", "  ")
			display.Println(display.Cyan, "Request Body:")
			display.Println(display.Reset, "%s", string(prettyJSON))
		} else {
			display.Print(display.Blue, "%s\n", bodyStr)
		}
	}

	// Execute request
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		display.Print(display.Red, "[ERROR] %s\n", err.Error())
		return err
	}
	defer resp.Body.Close()

	// Read response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	// Display response
	statusColor := display.Green
	if resp.StatusCode >= 400 {
		statusColor = display.Red
	}
	fmt.Printf("%s[%d %s]%s\n", statusColor, resp.StatusCode, http.StatusText(resp.StatusCode), display.Reset)

	// Display response body if verbose
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

	// Parse error response
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

	// Parse success response; a *string receives a non-JSON body as-is
	if text, ok := result.(*string); ok {
		*text = string(respBody)
		return nil
	}
	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			// For debug, show raw response if parsing fails
			display.Print(display.Red, "Response parse error: %s\n", err.Error())
			display.Print(display.Green, "Raw response: %s\n", string(respBody))
			return err
		}
	}

	return nil
}

// API Methods

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

func (c *Client) GetGameWithPoll(gameID string, moveCount int) (*GameResponse, error) {
	var resp GameResponse
	path := fmt.Sprintf("/api/games/%s?wait=true&moveCount=%d", gameID, moveCount)
	err := c.doRequest("GET", path, nil, &resp)
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

// RawRequest performs a raw HTTP request for debugging purposes
func (c *Client) RawRequest(method, path string, body string) error {
	var bodyData any
	if body != "" {
		if err := json.Unmarshal([]byte(body), &bodyData); err != nil {
			// Try as raw string
			bodyData = body
		}
	}

	return c.doRequest(method, path, bodyData, nil)
}
