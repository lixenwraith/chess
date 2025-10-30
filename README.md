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

Go backend server providing a RESTful API for chess gameplay. Integrates Stockfish engine for move validation and AI opponents.

## Features

- RESTful API for chess operations
- Stockfish engine integration for validation and AI
- Human vs human, human vs computer, computer vs computer modes
- Custom FEN position support
- Asynchronous AI move calculation
- Configurable AI strength and thinking time

## Requirements

- Go 1.24+
- Stockfish chess engine (`stockfish` in PATH)

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

# Standard mode (1 request/second/IP)
./chessd

# Development mode (10 requests/second/IP)
./chessd -dev

# Run tests (requires dev mode)
./test/test-api.sh
```

Server listens on `http://localhost:8080`. See [API Reference](./doc/api.md) for endpoints.

## Documentation

- [API Reference](./doc/api.md) - Endpoint specifications
- [Architecture](./doc/architecture.md) - System design
- [Development](./doc/development.md) - Build and test instructions
- [Stockfish Integration](./doc/stockfish.md) - Engine communication

## License

BSD 3-Clause