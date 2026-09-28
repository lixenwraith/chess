package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Queries for the integrity sweep. None of them change data except the two
// Delete methods.

// ExistingGames reports which of ids have a games row. Flush first when ids
// include games whose insert may still be queued.
func (s *Store) ExistingGames(ids []string) (map[string]bool, error) {
	exists := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return exists, nil
	}
	ctx, cancel := opContext()
	defer cancel()
	rows, err := s.db.QueryContext(ctx,
		`SELECT game_id FROM games WHERE game_id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("query existing games: %w", err)
	}
	return exists, collectIDs(rows, func(id string) { exists[id] = true })
}

// GamesWithMoveGaps returns games whose moves are not numbered 1..n: a move
// row was deleted from the start or the middle of the line.
func (s *Store) GamesWithMoveGaps() ([]string, error) {
	ctx, cancel := opContext()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT game_id FROM moves
		GROUP BY game_id HAVING count(*) <> max(move_number) ORDER BY game_id`)
	if err != nil {
		return nil, fmt.Errorf("query move gaps: %w", err)
	}
	var ids []string
	return ids, collectIDs(rows, func(id string) { ids = append(ids, id) })
}

// GameIDsAfter returns up to limit game IDs above after, in ID order; an
// empty after starts at the lowest.
func (s *Store) GameIDsAfter(after string, limit int) ([]string, error) {
	if after == "" {
		after = "00000000-0000-0000-0000-000000000000"
	} else if !validUUID(after) {
		return nil, fmt.Errorf("invalid game ID %q", after)
	}
	ctx, cancel := opContext()
	defer cancel()
	rows, err := s.db.QueryContext(ctx,
		`SELECT game_id FROM games WHERE game_id > $1 ORDER BY game_id LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("query game IDs: %w", err)
	}
	var ids []string
	return ids, collectIDs(rows, func(id string) { ids = append(ids, id) })
}

// OrphanedGames returns games that registered users claimed but whose
// claimants were all deleted, idle since cutoff by the anonymous-game rule
// (start, last move, and end all before it), excluding live games.
func (s *Store) OrphanedGames(cutoff time.Time, live []string) ([]string, error) {
	if live == nil {
		live = []string{}
	}
	ctx, cancel := opContext()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT g.game_id FROM games g
		WHERE (g.white_claimed_by IS NOT NULL OR g.black_claimed_by IS NOT NULL)
			AND NOT EXISTS (
				SELECT 1 FROM users u
				WHERE u.user_id IN (g.white_claimed_by, g.black_claimed_by)
			)
			AND g.start_time_utc < $1
			AND (g.end_time_utc IS NULL OR g.end_time_utc < $1)
			AND NOT EXISTS (
				SELECT 1 FROM moves m
				WHERE m.game_id = g.game_id AND m.move_time_utc >= $1
			)
			AND NOT (g.game_id = ANY($2::uuid[]))
		ORDER BY g.game_id`, cutoff.UTC(), live)
	if err != nil {
		return nil, fmt.Errorf("query orphaned games: %w", err)
	}
	var ids []string
	return ids, collectIDs(rows, func(id string) { ids = append(ids, id) })
}

// DeleteGames queues deletion of games and, by cascade, their moves. It runs
// through the ordered writer, after every write queued before it.
func (s *Store) DeleteGames(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return s.enqueue("delete_games", "", func(ctx context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM games WHERE game_id = ANY($1::uuid[])`, ids)
		if err != nil {
			return err
		}
		if deleted, err := result.RowsAffected(); err == nil {
			slog.Info("storage games deleted", "count", deleted)
		}
		return nil
	})
}

// DeleteUsers deletes accounts and, by cascade, their sessions. Their games
// keep the claims and name snapshots.
func (s *Store) DeleteUsers(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	result, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE user_id = ANY($1::uuid[])`, ids)
	if err != nil {
		return fmt.Errorf("delete users: %w", err)
	}
	if deleted, err := result.RowsAffected(); err == nil {
		slog.Info("storage users deleted", "count", deleted)
	}
	return nil
}

func collectIDs(rows *sql.Rows, add func(string)) error {
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		add(id)
	}
	if err := rows.Err(); err != nil {
		return errors.Join(errors.New("iterate game IDs"), err)
	}
	return nil
}
