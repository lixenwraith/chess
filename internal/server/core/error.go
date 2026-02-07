package core

// Error codes
const (
	ErrGameNotFound      = "GAME_NOT_FOUND"
	ErrInvalidMove       = "INVALID_MOVE"
	ErrNotHumanTurn      = "NOT_HUMAN_TURN"
	ErrGameOver          = "GAME_OVER"
	ErrRateLimitExceeded = "RATE_LIMIT_EXCEEDED"
	ErrInvalidContent    = "INVALID_CONTENT_TYPE"
	ErrInvalidRequest    = "INVALID_REQUEST"
	ErrInvalidFEN        = "INVALID_FEN"
	ErrInternalError     = "INTERNAL_ERROR"
	ErrResourceLimit     = "RESOURCE_LIMIT"
	ErrUnauthorized      = "UNAUTHORIZED"
)