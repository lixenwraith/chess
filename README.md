<table>
  <tr>
    <td>
      <h1>Go Chess API</h1>
      <p>
        <a href="https://golang.org"><img src="https://img.shields.io/badge/Go-1.25-00ADD8?style=flat&logo=go" alt="Go"></a>
        <a href="https://opensource.org/licenses/BSD-3-Clause"><img src="https://img.shields.io/badge/License-BSD_3--Clause-blue.svg" alt="License"></a>
      </p>
    </td>
  </tr>
</table>

# Chess

Go backend server providing a RESTful API for chess gameplay with user authentication. Integrates Stockfish engine for move validation and computer opponents.

## Features

- RESTful API for chess operations
- User registration and JWT authentication
- Stockfish engine integration for validation
- Human vs human, human vs computer, computer vs computer modes
- Custom FEN position support
- Asynchronous engine move calculation
- Configurable engine strength and thinking time
- SQLite persistence with async writes for games
- User management with secure Argon2 password storage
- PID file management for singleton enforcement
- Database CLI for storage and user administration

## Requirements

- Go 1.25+
- Stockfish chess engine (`stockfish` in PATH)
- SQLite3 (for persistence features)

### Installation
```bash
# Arch Linux
yay -S stockfish

# FreeBSD
pkg install stockfish
```

## Quick Start

### Using Make (Recommended)
```bash
git clone https://github.com/lixenwraith/chess
cd chess
make build

# Standard mode with persistence and auth
make run-server

# Or run with web UI
make run-server-web

# Initialize database with user support
make db-init

# Add users via CLI
./bin/chess-server db user add -path db/chess.db -username alice -password AlicePass123
```

### Building Manually
```bash
#git clone https://git.lixen.com/lixen/chess # Mirror
git clone https://github.com/lixenwraith/chess
cd chess
go build ./cmd/chess-server

# Standard mode with persistence and auth
./chess-server -storage-path chess.db

# Development mode with all features
./chess-server -dev -storage-path chess.db -pid /tmp/chess-server.pid -pid-lock -port 9090

# Initialize database with user support
./chess-server db init -path chess.db

# Add users via CLI
./chess-server db user add -path chess.db -username alice -password AlicePass123
./chess-server db user list -path chess.db
```

Server listens on `http://localhost:8080`. See [API Reference](./doc/api.md) for endpoints including authentication.

## User Management

The chess server supports user accounts with secure authentication:

### Creating Users
```bash
# Add user with password
./chess-server db user add -path chess.db -username alice -email alice@example.com -password SecurePass123

# Interactive password prompt
./chess-server db user add -path chess.db -username bob -interactive

# Import with existing hash
./chess-server db user add -path chess.db -username charlie -hash '$argon2id$...'
```

### Managing Users
```bash
# List all users
./chess-server db user list -path chess.db

# Update password
./chess-server db user set-password -path chess.db -username alice -password NewPass456

# Update email
./chess-server db user set-email -path chess.db -username alice -email newemail@example.com

# Delete user
./chess-server db user delete -path chess.db -username alice
```

## Web UI

The chess server includes an embedded web UI for playing games through a browser.

### Enabling Web UI
```bash
# Start with web UI on default port 9090
./chess-server -serve

# Custom web UI port  
./chess-server -serve -web-port 3000 -web-host 0.0.0.0

# Full example with authentication enabled
./chess-server -dev -serve -web-port 9090 -api-port 8080 -storage-path chess.db
```

### Features
- Visual chess board with drag-and-drop moves
- Human vs Computer gameplay
- Configurable engine strength (0-20)
- Move history with algebraic notation
- FEN display and custom starting positions
- Real-time server health monitoring
- User authentication support
- Responsive design for mobile devices

Access the UI at `http://localhost:9090` when server is running with `-serve` flag.

## Documentation

- [API Reference](./doc/api.md) - Endpoint specifications including auth
- [Architecture](./doc/architecture.md) - System design with auth layer
- [Development](./doc/development.md) - Build, test, and user management
- [Client Guide](./doc/client.md) - Interactive debugging client
- [Stockfish Integration](./doc/stockfish.md) - Engine communication

## License

BSD 3-Clause