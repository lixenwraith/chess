<table>
  <tr>
    <td>
      <h1>Go Chess API</h1>
      <p>
        <a href="https://golang.org"><img src="https://img.shields.io/badge/Go-1.24-00ADD8?style=flat&logo=go" alt="Go"></a>
        <a href="https://opensource.org/licenses/BSD-3-Clause"><img src="https://img.shields.io/badge/License-BSD_3--Clause-blue.svg" alt="License"></a>
      </p>
    </td>
  </tr>
</table>

# Chess

Go backend server providing a RESTful API for chess gameplay. Integrates Stockfish engine for move validation and computer opponents.

## Features

- RESTful API for chess operations
- Stockfish engine integration for validation
- Human vs human, human vs computer, computer vs computer modes
- Custom FEN position support
- Asynchronous engine move calculation
- Configurable engine strength and thinking time
- Optional SQLite persistence with async writes
- PID file management for singleton enforcement
- Database CLI for storage administration

## Requirements

- Go 1.24+
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

```bash
git clone https://git.lixen.com/lixen/chess
cd chess
go build ./cmd/chessd

# Standard mode with persistence
./chessd -storage-path chess.db

# Development mode with PID lock on localhost custom port
./chessd -dev -pid /tmp/chessd.pid -pid-lock -port 9090

# Database initialization (doesn't run server)
./chessd db init -path chess.db

# Query stored games (doesn't run server)
./chessd db query -path chess.db -gameId "*"
```

Server listens on `http://localhost:8080`. See [API Reference](./doc/api.md) for endpoints.

## Web UI

The chess server includes an embedded web UI for playing games through a browser.

### Enabling Web UI
```bash
# Start with web UI on default port 9090
./chessd -serve

# Custom web UI port  
./chessd -serve -web-port 3000 -web-host 0.0.0.0

# Full example with all features
./chessd -dev -serve -web-port 9090 -api-port 8080 -storage-path chess.db
```

### Features
- Visual chess board with drag-and-drop moves
- Human vs Computer gameplay
- Configurable engine strength (0-20)
- Move history with algebraic notation
- FEN display and custom starting positions
- Real-time server health monitoring
- Responsive design for mobile devices

Access the UI at `http://localhost:9090` when server is running with `-serve` flag.

## Documentation

- [API Reference](./doc/api.md) - Endpoint specifications
- [Architecture](./doc/architecture.md) - System design
- [Development](./doc/development.md) - Build and test instructions
- [Stockfish Integration](./doc/stockfish.md) - Engine communication

## License

BSD 3-Clause