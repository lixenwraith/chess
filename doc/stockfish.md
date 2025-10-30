# Stockfish Integration

## UCI Protocol Implementation

### Connection Management
Engine process started via `exec.Command("stockfish")` with bidirectional pipes. Initialization sequence:
1. Send `uci` → await `uciok`
2. Send `isready` → await `readyok`
3. Engine ready for commands

### Commands Used

#### Position Setting
```
position fen <fen_string> [moves <move1> <move2> ...]
```
Sets board state for validation or search.

#### Move Search
```
go movetime <milliseconds>
```
Calculates best move with time constraint. Returns:
- `info depth X score cp Y pv ...` (evaluation info)
- `bestmove <move>` (final result)

#### Board State Query
```
d
```
Debug command returning board visualization and FEN. Used for move validation.

#### Configuration
```
setoption name Skill Level value <0-20>
```
Sets engine strength for computer players.

### Response Parsing

#### Search Results
```go
type SearchResult struct {
    BestMove string  // UCI format move
    Score    int     // Centipawns or mate distance
    Depth    int     // Search depth reached
    IsMate   bool    // Checkmate detected
    MateIn   int     // Moves to mate
}
```

Parse `info` lines for evaluation data, `bestmove` for move selection.

#### FEN Extraction
Parse `d` output for line starting with `Fen: ` to get canonical position.

### Application Usage

#### Synchronous Validation (Processor)
Single mutex-protected engine instance validates moves:
1. Set position with current FEN
2. Attempt move
3. Get new FEN via `d` command
4. Compare FENs to determine legality

#### Asynchronous Calculation (EngineQueue)
Worker pool with dedicated engines per worker:
1. Receive task with FEN and time limit
2. Configure skill level
3. Search for best move
4. Return result via callback

### Error Handling

- Timeout protection (2x search time + 1s buffer)
- Process lifecycle management with graceful shutdown
- Fallback to force kill if quit fails
- "(none)" bestmove indicates no legal moves (checkmate/stalemate)

### Performance Considerations

- Reuse engine instances across multiple games
- `ucinewgame` between games for cache clearing
- Separate engines for validation vs calculation to avoid contention
- Fixed worker pool prevents resource exhaustion