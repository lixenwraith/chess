# Chess CLI Client Documentation

The chess client is an interactive command-line debugging tool for the chess server API. It provides a rich terminal interface with colored output, command completion, and session management.

## Features

- Interactive shell with readline support and command history
- Comprehensive command set for game operations and debugging
- User authentication with JWT token management
- Colored board visualization and game state display
- Session persistence across commands
- Verbose mode for detailed API request/response inspection
- Long-polling support for real-time game updates

## Building
```bash
go build ./cmd/chess-client-cli
# or with make: make build client
```

## Running
```bash
# Connect to default server (localhost:8080)
./chess-client-cli
# if built with make: bin/chess-client-cli

# The client starts with an interactive prompt
chess > 
```

### Prompt States

The prompt dynamically updates to show context:
- `chess` - Base prompt
- `chess [username]` - Authenticated user
- `chess [username - gameId]` - User in active game
- `chess [username - gameId] White` - Shows player color
- Turn indicator with player type (h=human, c=computer)

## Command Reference

### Authentication Commands

#### `register` / `r`
Register new user account.
```
chess > register
Username: alice
Password: ********
Email (optional): alice@example.com
```

#### `login` / `l`
Authenticate with existing account.
```
chess > login
Username or Email: alice
Password: ********
```

#### `logout` / `o`
Clear authentication token.
```
chess > logout
```

#### `whoami` / `i`
Display current authenticated user.
```
chess > whoami
```

#### `user` / `e`
Set user ID manually (for display only, doesn't authenticate).
```
chess > user 550e8400-e29b-41d4-a716-446655440000
```

### Game Commands

#### `new` / `n`
Create new game interactively.
```
chess > new
White player type (h/c) [h]: h
Black player type (h/c) [h]: c
Computer level (0-20) [10]: 15
Search time (100-10000ms) [1000]: 2000
Starting position (FEN) [default]: 
```

#### `join` / `j`
Set current game context.
```
chess > join a1b2c3d4-e5f6-7890-1234-567890abcdef
```

#### `move` / `m`
Make chess move in UCI notation.
```
chess > move e2e4
chess > move e7e5
```

#### `computer` / `c`
Trigger computer move calculation.
```
chess > computer
```

#### `undo` / `u`
Undo one or more moves.
```
chess > undo       # Undo last move
chess > undo 3     # Undo last 3 moves
```

#### `show` / `h`
Display board and game state with colored pieces.
```
chess > show
```

#### `state` / `s`
Show raw game JSON response.
```
chess > state
```

#### `delete` / `d`
Delete game from server.
```
chess > delete              # Delete current game
chess > delete <gameId>      # Delete specific game
```

#### `poll` / `p`
Long-poll for game updates (waits up to 25 seconds).
```
chess > poll
```

### Debug Commands

#### `health` / `.`
Check server health status.
```
chess > health
```

#### `url` / `/`
Get or set API base URL.
```
chess > url                           # Show current URL
chess > url http://localhost:9090     # Change server URL
```

#### `raw` / `:`
Send raw API request.
```
chess > raw GET /api/v1/games/<id>
chess > raw POST /api/v1/games '{"white":{"type":1},"black":{"type":2}}'
```

#### `clear` / `-`
Clear terminal screen.
```
chess > clear
```

#### `help` / `?`
Display available commands or specific command usage.
```
chess > help          # Show all commands
chess > help move     # Show move command details
```

#### `exit` / `x`
Exit the client.
```
chess > exit
```

### Verbose Mode

Append `-v` flag to any command for detailed output:
```
chess > move e2e4 -v
```

Shows full HTTP request/response with formatted JSON bodies.

## Session Management

The client maintains session state including:
- API base URL
- Current game ID
- Authentication token
- Username and user ID
- Last move count (for polling)
- Current game state
- Player color assignment

Session state persists across commands within the same client instance.

## Display Features

### Colored Output
- **Blue**: White pieces, API requests, prompts
- **Red**: Black pieces, errors
- **Green**: Success messages
- **Yellow**: Warnings, input prompts
- **Magenta**: Computer moves, usernames
- **Cyan**: Information, file coordinates
- **White**: Game IDs

### Board Visualization
ASCII board with colored pieces:
```
  a b c d e f g h
8 r n b q k b n r 8
7 p p p p p p p p 7
6 . . . . . . . . 6
5 . . . . . . . . 5
4 . . . . P . . . 4
3 . . . . . . . . 3
2 P P P P . P P P 2
1 R N B Q K B N R 1
  a b c d e f g h
```

### Move History
Displayed in algebraic notation with move numbers:
```
History: 1.e4 e5 2.Nf3 Nc6 3.Bb5
```

## Workflows

### Authenticated Game Creation
1. Register or login to obtain token
2. Create game (human players automatically associated with user)
3. Make moves with authentication context
4. Games persist with user association

### Computer vs Computer Observation
1. Create game with both players as computer
2. Use polling to watch moves in real-time
3. Board updates automatically as engines calculate

### Debug Server Testing
1. Set verbose mode for request inspection
2. Use raw command for custom API calls
3. Health endpoint for connectivity verification
4. URL switching for multi-server testing

## Configuration

### Environment
- History file: `.chess_history` in working directory
- Default API URL: `http://localhost:8080`
- Timeout: 30 seconds for HTTP requests

### Limitations
- No move validation in client (server validates)
- No algebraic notation input (UCI format only)
- Single game context at a time
- No persistent authentication across restarts

## Error Handling

The client displays server errors with color coding:
- Red text for error messages
- Error codes for specific failures
- Details when available

Common error codes:
- `GAME_NOT_FOUND` - Invalid game ID
- `INVALID_MOVE` - Illegal chess move
- `NOT_HUMAN_TURN` - Wrong player type
- `GAME_OVER` - Game already ended
- `RATE_LIMIT_EXCEEDED` - Too many requests

## Development

The client architecture follows a command pattern with:
- **Registry**: Central command dispatcher
- **Session**: State management interface
- **API Client**: HTTP communication layer
- **Display**: Terminal formatting utilities
- **Commands**: Modular command handlers

Extensions can add new commands by registering handlers in the appropriate command group (game, auth, debug).