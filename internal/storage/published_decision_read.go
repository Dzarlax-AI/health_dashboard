package storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// withPublishedDecision isolates optional publication lookups on older databases.
// A failed lookup inside an ongoing write must not abort its outer transaction.
func withPublishedDecision[T any](ctx context.Context, s *DB, read func(*DB) (T, bool, error)) (T, bool, error) {
	var result T
	if s.profileGraphSchema != "" {
		return result, false, nil
	}
	var found bool
	var err error
	if tx, ok := s.pool.(*cacheTransaction); ok {
		err = pgx.BeginFunc(ctx, tx, func(save pgx.Tx) error {
			var lookupErr error
			result, found, lookupErr = read(&DB{pool: &cacheTransaction{Tx: save, ctx: ctx}})
			return lookupErr
		})
	} else {
		result, found, err = read(s)
	}
	var pgError *pgconn.PgError
	if errors.Is(err, pgx.ErrNoRows) || (errors.As(err, &pgError) && pgError.Code == "42P01") {
		return result, false, nil
	}
	return result, found, err
}
