# Stockfish UCI Protocol Reference

## UCI Protocol Overview

Universal Chess Interface (UCI) is a text-based protocol for communication between chess engines and GUIs. Commands are line-based with ASCII encoding.

## Initialization Sequence

```
→ uci
← id name Stockfish 16
← id author Stockfish developers
← option name Debug Log File type string default
← option name Threads type spin default 1 min 1 max 1024
← option name Hash type spin default 16 min 1 max 33554432
← [... more options ...]
← uciok

→ isready
← readyok
```

## Core UCI Commands

### Engine Identification
- `uci` - Initialize UCI mode, engine responds with options and `uciok`
- `quit` - Terminate engine process

### Synchronization
- `isready` - Synchronization command, engine responds `readyok` when ready
- `ucinewgame` - Clear hash tables and reset for new game

### Position Setup
```
position [fen <fenstring> | startpos] [moves <move1> ... <moveN>]
```
- `startpos` - Standard starting position
- `fen <fenstring>` - Custom position in FEN notation
- `moves` - Apply moves from position in UCI format (e.g., e2e4, e7e8q)

### Search Commands

#### Basic Search
```
go [searchmoves <move1> ... <moveN>] [ponder] [wtime <ms>] [btime <ms>]
   [winc <ms>] [binc <ms>] [movestogo <n>] [depth <n>] [nodes <n>]
   [mate <n>] [movetime <ms>] [infinite]
```

Parameters:
- `searchmoves` - Restrict search to specific moves
- `ponder` - Start pondering mode (thinking on opponent's time)
- `wtime/btime` - White/black time remaining (ms)
- `winc/binc` - White/black increment per move (ms)
- `movestogo` - Moves until next time control
- `depth` - Search to fixed depth
- `nodes` - Search fixed number of positions
- `mate` - Search for mate in N moves
- `movetime` - Search for fixed time (ms)
- `infinite` - Search until `stop` command

#### Search Control
- `stop` - Stop calculating and return best move
- `ponderhit` - Opponent played expected ponder move

### Engine Options
```
setoption name <option_name> [value <value>]
```

## Stockfish-Specific Options

### Search Parameters
- `MultiPV` (1-500): Number of principal variations to calculate
- `Skill Level` (0-20): Playing strength limitation
- `Contempt` (-100 to 100): Draw avoidance tendency
- `Analysis Contempt` (Off/White/Black/Both): Contempt perspective
- `Move Overhead` (0-5000ms): Time buffer for network/GUI delay
- `Slow Mover` (10-1000): Time management aggressiveness
- `UCI_AnalyseMode` (true/false): Optimization for analysis
- `UCI_Chess960` (true/false): Fischer Random Chess support
- `UCI_ShowWDL` (true/false): Show win/draw/loss probabilities
- `UCI_LimitStrength` (true/false): Enable ELO limitation
- `UCI_Elo` (1320-3190): Target ELO when strength limited

### Hash Tables
- `Hash` (1-33554432 MB): Transposition table size
- `Clear Hash`: Clear transposition table
- `Ponder` (true/false): Think during opponent's turn

### Hardware Configuration
- `Threads` (1-1024): Search threads (typically CPU cores)
- `Use NNUE` (true/false): Neural network evaluation
- `EvalFile` (path): Custom NNUE evaluation file

### Syzygy Tablebases
- `SyzygyPath` (path): Directory containing tablebase files
- `SyzygyProbeDepth` (1-100): Minimum depth for tablebase probing
- `Syzygy50MoveRule` (true/false): Consider 50-move rule
- `SyzygyProbeLimit` (0-7): Maximum pieces for probing

## Debug Commands

### Board Display
```
→ d
← 
 +---+---+---+---+---+---+---+---+
 | r | n | b | q | k | b | n | r | 8
 +---+---+---+---+---+---+---+---+
 | p | p | p | p | p | p | p | p | 7
 +---+---+---+---+---+---+---+---+
 |   |   |   |   |   |   |   |   | 6
 [...]
 +---+---+---+---+---+---+---+---+
   a   b   c   d   e   f   g   h

Fen: rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1
Key: 8F8F01D4562F59FB
Checkers:
```

### Evaluation
```
→ eval
← Total evaluation: +0.25 (white side)
← [detailed NNUE evaluation breakdown]
```

### Performance Testing
```
→ bench [depth] [threads] [hash] [fenfile] [limittype] [evaltype]
```
Default: `bench 13 1 16 default depth mixed`

## Search Output Format

### Standard Info String
```
info depth <d> seldepth <sd> multipv <n> score <score> nodes <n> 
     nps <n> hashfull <n> tbhits <n> time <ms> pv <move1> ... <moveN>
```

Fields:
- `depth` - Current search depth
- `seldepth` - Selective search depth
- `multipv` - PV number (when MultiPV > 1)
- `score cp <n>` - Evaluation in centipawns
- `score mate <n>` - Mate in N moves (negative if being mated)
- `score lowerbound/upperbound` - Bound type in fail-high/low
- `nodes` - Nodes searched
- `nps` - Nodes per second
- `hashfull` - Hash table saturation (per mill)
- `tbhits` - Tablebase positions found
- `time` - Search time (ms)
- `pv` - Principal variation (best line)
- `currmove` - Currently searching move
- `currmovenumber` - Move number in root move list
- `string` - Free-form engine output

### Win/Draw/Loss Output (UCI_ShowWDL=true)
```
info depth 20 score cp 15 wdl 395 604 1
```
WDL values in per mill: win/draw/loss from current side's perspective.

### Multi-PV Example
```
setoption name MultiPV value 3
go depth 15

info multipv 1 depth 15 score cp 31 pv e2e4 e7e5 g1f3
info multipv 2 depth 15 score cp 20 pv d2d4 d7d5 g1f3
info multipv 3 depth 15 score cp 15 pv g1f3 g8f6 d2d4
```

## Best Move Output
```
bestmove <move> [ponder <move>]
```
- `bestmove` - Best move in UCI notation
- `ponder` - Expected opponent response for pondering

Special cases:
- `bestmove (none)` - No legal moves (checkmate/stalemate)
- `bestmove 0000` - Null move (analysis mode only)

## Advanced Analysis Techniques

### Infinite Analysis
```
position fen <position>
setoption name UCI_AnalyseMode value true
go infinite
[... engine thinks until stop ...]
stop
```

### Multi-PV Analysis
```
setoption name MultiPV value 5
position startpos moves e2e4 e7e5
go depth 20
```

### Mate Search
```
go mate 7  # Find mate in 7 moves or less
```

### Fixed Node Search
```
go nodes 1000000  # Analyze exactly 1M positions
```

### Search Move Restriction
```
position startpos
go searchmoves e2e4 d2d4 g1f3  # Only consider these moves
```

## Time Management

### Tournament Time Control
```
position startpos moves e2e4 e7e5
go wtime 300000 btime 300000 winc 2000 binc 2000 movestogo 40
```
5 minutes + 2 second increment, 40 moves to time control.

### Sudden Death
```
go wtime 60000 btime 60000  # 1 minute each, no increment
```

### Fixed Time Per Move
```
go movetime 5000  # Think for exactly 5 seconds
```

## Performance Tuning

### Analysis Optimization
```
setoption name Threads value 8
setoption name Hash value 4096
setoption name UCI_AnalyseMode value true
setoption name MultiPV value 1
```

### Rapid/Blitz Optimization
```
setoption name Move Overhead value 100
setoption name Slow Mover value 50
setoption name Threads value 4
```

### Endgame Optimization
```
setoption name SyzygyPath value /path/to/tablebases
setoption name SyzygyProbeDepth value 1
```

## Error Handling

Common error responses:
- `Unknown command: <cmd>` - Invalid UCI command
- `Illegal move: <move>` - Move not legal in current position
- `Invalid position` - FEN parsing failed
- `No such option: <name>` - Unknown engine option

## Protocol Extensions

### Chess960 (Fischer Random)
```
setoption name UCI_Chess960 value true
position fen <chess960_fen> moves <move1> ...
```

### Debug Logging
```
setoption name Debug Log File value debug.txt
setoption name Use Debug Log value true
```

### NNUE Evaluation
```
setoption name Use NNUE value true
setoption name EvalFile value nn-[hash].nnue
```

## Typical Usage Patterns

### Game Analysis
1. Set analysis mode and resources
2. Load position with game moves
3. Run infinite analysis
4. Stop and retrieve evaluation

### Opening Preparation
1. Set MultiPV to compare variations
2. Load opening position
3. Search to fixed depth
4. Compare evaluations of candidate moves

### Endgame Study
1. Configure tablebase paths
2. Load endgame position
3. Search for mate or optimal play
4. Verify with tablebase hits

### Engine Match
1. Reset with ucinewgame
2. Set time controls
3. Apply moves incrementally
4. Use ponder for thinking on opponent time