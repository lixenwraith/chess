<table>
  <tr>
    <td>
      <h1>Go Chess</h1>
      <p>
        <a href="https://golang.org"><img src="https://img.shields.io/badge/Go-1.24-00ADD8?style=flat&logo=go" alt="Go"></a>
        <a href="https://opensource.org/licenses/BSD-3-Clause"><img src="https://img.shields.io/badge/License-BSD_3--Clause-blue.svg" alt="License"></a>
      </p>
    </td>
  </tr>
</table>

# Go Chess

A command-line chess application written in Go.

## Features

*   Command-line interface for gameplay.
*   Uses an stockfish external chess engine for move validation and computer play.
*   Supports player vs. player, player vs. computer, and computer vs. computer modes.
*   Start a new game from the standard starting position.
*   Resume a game from a FEN (Forsyth-Edwards Notation) string.
*   Move history display.
*   Move undo functionality.

## System Requirements

*   **Go Version**: 1.24+ (for building from source)
*   **Engine**: Requires the **Stockfish** chess engine to be installed. The `stockfish` executable must be available in the system's PATH.

## Quick Start

To build and run the application:

```sh
# Build the executable
go build ./cmd/chess

# Run the application
./chess
```

Inside the application, type `help` to see available commands.

## License

BSD 3-Clause License
