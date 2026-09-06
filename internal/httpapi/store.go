package httpapi

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/legendary1205/rapido-go/internal/db/generated"
)

type Store struct {
	Pool    *pgxpool.Pool
	Queries *generated.Queries
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{Pool: pool, Queries: generated.New(pool)}
}

// ResolveAdmin implements auth.Resolver against the real admins table.
func (s *Store) ResolveAdmin(ctx context.Context, username string) (adminID int32, isSudo bool, passwordResetAt *time.Time, found bool, err error) {
	admin, err := s.Queries.GetAdminByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil, false, nil
		}
		return 0, false, nil, false, err
	}
	if admin.PasswordResetAt.Valid {
		t := admin.PasswordResetAt.Time
		passwordResetAt = &t
	}
	return admin.ID, admin.IsSudo, passwordResetAt, true, nil
}
