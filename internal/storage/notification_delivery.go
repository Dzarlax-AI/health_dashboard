package storage

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const notificationDeliveriesTableDDL = `CREATE TABLE IF NOT EXISTS notification_deliveries (
	delivery_key TEXT PRIMARY KEY,
	status TEXT NOT NULL CHECK (status IN ('reserved','sent','ambiguous','failed')),
	lease_token UUID NOT NULL,
	reserved_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	completed_at TIMESTAMPTZ,
	error_code TEXT
)`

// ReserveNotificationDelivery implements an at-most-once external-send gate.
// A crash after reservation intentionally leaves the row reserved: automatic
// retry could duplicate a delivery whose provider response was lost.
func (s *DB) ReserveNotificationDelivery(ctx context.Context, key string) (uuid.UUID, bool, error) {
	if key == "" {
		return uuid.Nil, false, errors.New("delivery key is required")
	}
	if _, err := s.pool.Exec(ctx, notificationDeliveriesTableDDL); err != nil {
		return uuid.Nil, false, err
	}
	token := uuid.New()
	var got uuid.UUID
	err := s.pool.QueryRow(ctx, `
		INSERT INTO notification_deliveries(delivery_key,status,lease_token)
		VALUES($1,'reserved',$2)
		ON CONFLICT(delivery_key) DO UPDATE SET
			status='reserved',lease_token=excluded.lease_token,reserved_at=NOW(),completed_at=NULL,error_code=NULL
		WHERE notification_deliveries.status='failed'
		RETURNING lease_token`, key, token).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	return got, err == nil, err
}

func (s *DB) CompleteNotificationDelivery(ctx context.Context, key string, token uuid.UUID, status, errorCode string) error {
	if status != "sent" && status != "ambiguous" && status != "failed" {
		return errors.New("invalid delivery completion status")
	}
	completedAt := any(time.Now())
	if status == "failed" {
		completedAt = nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE notification_deliveries SET status=$3,completed_at=$4,error_code=NULLIF($5,'') WHERE delivery_key=$1 AND lease_token=$2 AND status='reserved'`, key, token, status, completedAt, errorCode)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("delivery lease is no longer owned")
	}
	return nil
}
