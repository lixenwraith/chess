<table>
  <tr>
    <td>
      <h1>♚♛♜♝♞</h1>
      <p>
        <a href="https://golang.org"><img src="https://img.shields.io/badge/Go-1.27-00ADD8?style=flat&logo=go" alt="Go 1.27"></a>
        <a href="https://opensource.org/licenses/BSD-3-Clause"><img src="https://img.shields.io/badge/License-BSD_3--Clause-blue.svg" alt="License BSD-3"></a>
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
- Resignation, draw offers (the computer answers from its evaluation), and
  automatic draws: dead material, threefold repetition, fifty-move rule
- Claimed sides protect players: no takebacks between two players, and a
  resignation or agreed draw between humans is final; against the computer it
  can be undone and stays on record
- Custom FEN position support
- Asynchronous engine move calculation
- Configurable engine strength and thinking time
- PostgreSQL 18 persistence with ordered async writes for games
- Durable game results, player claims and names, ordered move history, and replay API
- Dependency-free rules core: SAN history, PGN export, FEN validation, and a
  shadow check of every engine-validated move
- Games without a registered player are purged 24 hours after their last activity
- Authenticated stored-game listing with cursor pagination and filters
- Configurable structured debug logs for persistence, cleanup, and engine work
- User management with secure Argon2id password storage and scoped JWTs
- Static, CGO-free binaries (cross-compiles for FreeBSD)
- Scripted deployment: FreeBSD jail (rc.d) or Linux (sandboxed systemd unit)
- Optional hourly integrity sweep of stored games and accounts
- PID file management for singleton enforcement
- Database CLI for storage and user administration

## Requirements

- Go 1.27+
- Stockfish chess engine (`stockfish` in PATH)
- PostgreSQL 17+, 18 recommended (for persistence and accounts; see
  [FreeBSD](./doc/deployment.md) or [Linux](./doc/deployment-linux.md) deployment)

### Installation
```bash
# Arch Linux
yay -S stockfish

# FreeBSD
pkg install stockfish postgresql18-server
```

The connection string comes from `-dsn` or `CHESS_DSN`, in libpq keyword/value
or URL form. With a local socket and peer authentication, `dbname=chess` is
enough. Unset fields fall back to the standard `PG*` variables and `~/.pgpass`.

## Quick Start

### Using Make (Recommended)
```bash
git clone https://github.com/lixenwraith/chess
cd chess
make build

export CHESS_DSN='dbname=chess'   # used by make targets and the CLI

# Create or migrate the schema (the server also does this at startup)
make db-init

# Standard mode with persistence and auth
make run-server

# Or run with web UI
make run-server-web

# View all build options
make help

# Add users via CLI
./bin/chess-server db user add -username alice -password AlicePass123
```

### Building Manually
```bash
#git clone https://git.lixen.com/lixen/chess # Mirror
git clone https://github.com/lixenwraith/chess
cd chess
go build ./cmd/chess-server

# Standard mode with persistence and auth
./chess-server -dsn 'dbname=chess' -jwt-secret-file ~/.chess-jwt.key

# Development mode with all features
./chess-server -dev -dsn 'dbname=chess' -pid /tmp/chess-server.pid -pid-lock -api-port 9090

# Detailed persistence and HTTP diagnostics
./chess-server -dev -dsn 'dbname=chess' -log-level debug -log-http=true

# Initialize the schema
./chess-server db init -dsn 'dbname=chess'

# Add users via CLI (-dsn defaults to $CHESS_DSN)
./chess-server db user add -dsn 'dbname=chess' -username alice -password AlicePass123
./chess-server db user list -dsn 'dbname=chess'
```

Server listens on `http://localhost:8080`. See [API Reference](./doc/api.md) for endpoints including authentication.

## User Management

The chess server supports user accounts with secure authentication. The
commands below read the connection string from `CHESS_DSN` (or `-dsn`).
Accounts registered on the site and accounts created here are identical and
do not expire. Games without a registered player are deleted 24 hours after
their last activity.

### Creating Users
```bash
# Add user with password
./chess-server db user add -username alice -email alice@example.com -password SecurePass123

# Interactive password prompt
./chess-server db user add -username bob -interactive

# Import with existing hash
./chess-server db user add -username charlie -hash '$argon2id$...'
```

### Managing Users
```bash
# List all users
./chess-server db user list

# Update password
./chess-server db user set-password -username alice -password NewPass456

# Update email
./chess-server db user set-email -username alice -email newemail@example.com

# Delete user
./chess-server db user delete -username alice
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
./chess-server -dev -serve -web-port 9090 -api-port 8080 -dsn 'dbname=chess'

# Override the API origin seen by browsers when it differs from the listen address
./chess-server -serve -web-api-url https://api.example.com
```

### Features
- Visual chess board with drag-and-drop moves
- Human vs Computer gameplay
- Configurable engine strength (0-20)
- UCI move history with move numbers
- FEN display and custom starting positions
- Real-time server health monitoring
- User authentication support
- Responsive design for mobile devices

Access the UI at `http://localhost:9090` when server is running with `-serve` flag.

## Documentation

- [API Reference](./doc/api.md) - Endpoint specifications including auth
- [Deployment](./doc/deployment.md) - FreeBSD jail, PostgreSQL 18, `chessd` service, nginx, web clients
- [Linux Deployment](./doc/deployment-linux.md) - Debian/Ubuntu/Arch, systemd service, development setup
- [Database Operations](./doc/database.md) - layout, psql as `postgres`, games, users, integrity sweep, starting fresh
- [Architecture](./doc/architecture.md) - System design with auth layer
- [Development](./doc/development.md) - Build, test, and user management
- [Client Guide](./doc/client.md) - Interactive debugging client
- [Replay Plan](./doc/todo.md) - Phased game-replay implementation and open decisions
- [Stockfish Integration](./doc/stockfish.md) - Engine communication

## License

BSD 3-Clause
