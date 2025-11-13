// FILE: lixenwraith/chess/internal/api/client.go
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"chess/internal/client/display"
)

type Client struct {
	BaseURL    string
	AuthToken  string
	HTTPClient *http.Client
	Verbose    bool
}

func New(baseURL string) *Client {
	return &Client{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
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

func (c *Client) doRequest(method, path string, body interface{}, result interface{}) error {
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
	fmt.Printf("\n%s[API] %s %s%s\n", display.Blue, method, path, display.Reset)
	if bodyStr != "" {
		if c.Verbose {
			// Display request body if verbose
			var prettyBody interface{}
			json.Unmarshal([]byte(bodyStr), &prettyBody)
			prettyJSON, _ := json.MarshalIndent(prettyBody, "", "  ")
			fmt.Printf("%sRequest Body:%s\n%s\n", display.Cyan, display.Reset, string(prettyJSON))
		} else {
			fmt.Printf("%s%s%s\n", display.Blue, bodyStr, display.Reset)
		}
	}

	// Execute request
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		fmt.Printf("%s[ERROR] %s%s\n", display.Red, err.Error(), display.Reset)
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
		var prettyResp interface{}
		if err := json.Unmarshal(respBody, &prettyResp); err == nil {
			prettyJSON, _ := json.MarshalIndent(prettyResp, "", "  ")
			fmt.Printf("%sResponse Body:%s\n%s\n", display.Cyan, display.Reset, string(prettyJSON))
		} else {
			fmt.Printf("%sResponse:%s\n%s\n", display.Cyan, display.Reset, string(respBody))
		}
	}

	// Parse error response
	if resp.StatusCode >= 400 {
		var errResp ErrorResponse
		if err := json.Unmarshal(respBody, &errResp); err == nil {
			if !c.Verbose {
				fmt.Printf("%sError: %s%s\n", display.Red, errResp.Error, display.Reset)
				if errResp.Code != "" {
					fmt.Printf("%sCode: %s%s\n", display.Red, errResp.Code, display.Reset)
				}
				if errResp.Details != "" {
					fmt.Printf("%sDetails: %s%s\n", display.Red, errResp.Details, display.Reset)
				}
			}
		} else if !c.Verbose {
			fmt.Printf("%s%s%s\n", display.Red, string(respBody), display.Reset)
		}
		return fmt.Errorf("request failed with status %d", resp.StatusCode)
	}

	// Parse success response
	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			// For debug, show raw response if parsing fails
			fmt.Printf("%sResponse parse error: %s%s\n", display.Red, err.Error(), display.Reset)
			fmt.Printf("%sRaw response: %s%s\n", display.Green, string(respBody), display.Reset)
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
	err := c.doRequest("POST", "/api/v1/games", req, &resp)
	return &resp, err
}

func (c *Client) GetGame(gameID string) (*GameResponse, error) {
	var resp GameResponse
	err := c.doRequest("GET", "/api/v1/games/"+gameID, nil, &resp)
	return &resp, err
}

func (c *Client) GetGameWithPoll(gameID string, moveCount int) (*GameResponse, error) {
	var resp GameResponse
	path := fmt.Sprintf("/api/v1/games/%s?wait=true&moveCount=%d", gameID, moveCount)
	err := c.doRequest("GET", path, nil, &resp)
	return &resp, err
}

func (c *Client) DeleteGame(gameID string) error {
	return c.doRequest("DELETE", "/api/v1/games/"+gameID, nil, nil)
}

func (c *Client) MakeMove(gameID string, move string) (*GameResponse, error) {
	req := &MoveRequest{Move: move}
	var resp GameResponse
	err := c.doRequest("POST", "/api/v1/games/"+gameID+"/moves", req, &resp)
	return &resp, err
}

func (c *Client) UndoMoves(gameID string, count int) (*GameResponse, error) {
	req := &UndoRequest{Count: count}
	var resp GameResponse
	err := c.doRequest("POST", "/api/v1/games/"+gameID+"/undo", req, &resp)
	return &resp, err
}

func (c *Client) GetBoard(gameID string) (*BoardResponse, error) {
	var resp BoardResponse
	err := c.doRequest("GET", "/api/v1/games/"+gameID+"/board", nil, &resp)
	return &resp, err
}

func (c *Client) Register(username, password, email string) (*AuthResponse, error) {
	req := &RegisterRequest{
		Username: username,
		Password: password,
		Email:    email,
	}
	var resp AuthResponse
	err := c.doRequest("POST", "/api/v1/auth/register", req, &resp)
	return &resp, err
}

func (c *Client) Login(identifier, password string) (*AuthResponse, error) {
	req := &LoginRequest{
		Identifier: identifier,
		Password:   password,
	}
	var resp AuthResponse
	err := c.doRequest("POST", "/api/v1/auth/login", req, &resp)
	return &resp, err
}

func (c *Client) GetCurrentUser() (*UserResponse, error) {
	var resp UserResponse
	err := c.doRequest("GET", "/api/v1/auth/me", nil, &resp)
	return &resp, err
}

// RawRequest performs a raw HTTP request for debugging purposes
func (c *Client) RawRequest(method, path string, body string) error {
	var bodyData interface{}
	if body != "" {
		if err := json.Unmarshal([]byte(body), &bodyData); err != nil {
			// Try as raw string
			bodyData = body
		}
	}

	return c.doRequest(method, path, bodyData, nil)
}