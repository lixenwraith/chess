// FILE: lixenwraith/chess/internal/client/session/session.go
package session

import (
	"chess/internal/client/api"
)

// Session maintains client state and configuration
type Session struct {
	APIBaseURL    string
	CurrentGame   string
	CurrentUser   string
	AuthToken     string
	Username      string
	LastMoveCount int
	Client        *api.Client
	Verbose       bool
	// Game state for prompt
	CurrentGameState *api.GameResponse
	PlayerColor      string // "w", "b", or ""
}

// Session interface implementation
func (s *Session) GetAPIBaseURL() string      { return s.APIBaseURL }
func (s *Session) SetAPIBaseURL(url string)   { s.APIBaseURL = url }
func (s *Session) GetCurrentGame() string     { return s.CurrentGame }
func (s *Session) SetCurrentGame(id string)   { s.CurrentGame = id }
func (s *Session) GetCurrentUser() string     { return s.CurrentUser }
func (s *Session) SetCurrentUser(id string)   { s.CurrentUser = id }
func (s *Session) GetAuthToken() string       { return s.AuthToken }
func (s *Session) SetAuthToken(token string)  { s.AuthToken = token }
func (s *Session) GetUsername() string        { return s.Username }
func (s *Session) SetUsername(name string)    { s.Username = name }
func (s *Session) GetLastMoveCount() int      { return s.LastMoveCount }
func (s *Session) SetLastMoveCount(count int) { s.LastMoveCount = count }
func (s *Session) GetClient() interface{}     { return s.Client }
func (s *Session) IsVerbose() bool            { return s.Verbose }
func (s *Session) SetGameState(game interface{}) {
	if g, ok := game.(*api.GameResponse); ok {
		s.CurrentGameState = g
	}
}
func (s *Session) SetPlayerColor(color string) { s.PlayerColor = color }
func (s *Session) GetPlayerColor() string      { return s.PlayerColor }