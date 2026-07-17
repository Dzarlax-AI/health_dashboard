package registry

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var schemaContractChecksumRE = regexp.MustCompile(`^[a-f0-9]{64}$`)

type SchemaContractMetadata struct {
	Version  int
	Checksum string
}

func (m SchemaContractMetadata) Validate() error {
	if m.Version <= 0 {
		return errors.New("schema contract version must be positive")
	}
	if !schemaContractChecksumRE.MatchString(m.Checksum) {
		return errors.New("schema contract checksum must be a lowercase SHA-256 digest")
	}
	return nil
}

type ProvisioningState string

const (
	ProvisioningStatePending      ProvisioningState = "pending"
	ProvisioningStateProvisioning ProvisioningState = "provisioning"
	ProvisioningStateActive       ProvisioningState = "active"
	ProvisioningStateFailed       ProvisioningState = "failed"
)

var ErrProvisioningStateConflict = errors.New("provisioning state conflict")

func detachedRegistryContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func (s ProvisioningState) Valid() bool {
	switch s {
	case ProvisioningStatePending, ProvisioningStateProvisioning, ProvisioningStateActive, ProvisioningStateFailed:
		return true
	default:
		return false
	}
}

func CanTransitionProvisioning(from, to ProvisioningState) bool {
	switch from {
	case ProvisioningStatePending:
		return to == ProvisioningStateProvisioning || to == ProvisioningStateFailed
	case ProvisioningStateProvisioning:
		return to == ProvisioningStateActive || to == ProvisioningStateFailed
	case ProvisioningStateFailed:
		return to == ProvisioningStateProvisioning
	default:
		return false
	}
}

type ProvisioningOperation struct {
	OperationID       uuid.UUID
	TenantID          uuid.UUID
	Username          string
	SchemaName        string
	DBRole            string
	CredentialVersion int
	State             ProvisioningState
	Error             string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (r *Registry) CreateProvisioningOperation(ctx context.Context, op ProvisioningOperation) error {
	if op.OperationID == uuid.Nil || op.TenantID == uuid.Nil {
		return errors.New("operation and tenant IDs must not be nil")
	}
	if op.State == "" {
		op.State = ProvisioningStatePending
	}
	if op.CredentialVersion == 0 {
		op.CredentialVersion = 1
	}
	if !op.State.Valid() || op.State == ProvisioningStateActive {
		return fmt.Errorf("invalid initial provisioning state %q", op.State)
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO health_registry.tenant_provisioning_operations
			(operation_id, tenant_id, username, schema_name, db_role, credential_version, state, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''))
	`, op.OperationID, op.TenantID, op.Username, op.SchemaName, op.DBRole, op.CredentialVersion, op.State, op.Error)
	if err != nil {
		return fmt.Errorf("create provisioning operation: %w", err)
	}
	return nil
}

func (r *Registry) GetProvisioningOperation(ctx context.Context, operationID uuid.UUID) (ProvisioningOperation, error) {
	var op ProvisioningOperation
	err := r.pool.QueryRow(ctx, `SELECT operation_id, tenant_id, username, schema_name, db_role, credential_version, state, COALESCE(error,''), created_at, updated_at FROM health_registry.tenant_provisioning_operations WHERE operation_id=$1`, operationID).Scan(
		&op.OperationID, &op.TenantID, &op.Username, &op.SchemaName, &op.DBRole, &op.CredentialVersion, &op.State, &op.Error, &op.CreatedAt, &op.UpdatedAt)
	if err != nil {
		return op, fmt.Errorf("get provisioning operation: %w", err)
	}
	return op, nil
}

func (r *Registry) ListNonterminalProvisioningOperations(ctx context.Context) ([]ProvisioningOperation, error) {
	rows, err := r.pool.Query(ctx, `SELECT operation_id, tenant_id, username, schema_name, db_role, credential_version, state, COALESCE(error,''), created_at, updated_at FROM health_registry.tenant_provisioning_operations WHERE state IN ('pending','provisioning') ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProvisioningOperation
	for rows.Next() {
		var op ProvisioningOperation
		if err := rows.Scan(&op.OperationID, &op.TenantID, &op.Username, &op.SchemaName, &op.DBRole, &op.CredentialVersion, &op.State, &op.Error, &op.CreatedAt, &op.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

func (r *Registry) TransitionProvisioningOperation(ctx context.Context, operationID uuid.UUID, from, to ProvisioningState, operationError string) error {
	if !CanTransitionProvisioning(from, to) {
		return fmt.Errorf("invalid provisioning transition %q -> %q", from, to)
	}
	var returned uuid.UUID
	err := r.pool.QueryRow(ctx, `
		UPDATE health_registry.tenant_provisioning_operations
		SET state = $2, error = NULLIF($3, ''), updated_at = NOW()
		WHERE operation_id = $1 AND state = $4
		RETURNING operation_id
	`, operationID, to, strings.TrimSpace(operationError), from).Scan(&returned)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrProvisioningStateConflict
	}
	if err != nil {
		return fmt.Errorf("transition provisioning operation: %w", err)
	}
	return nil
}

func (r *Registry) transitionUserAndOperation(ctx context.Context, operationID uuid.UUID, from, to ProvisioningState, operationError string) error {
	if !CanTransitionProvisioning(from, to) {
		return fmt.Errorf("invalid provisioning transition %q -> %q", from, to)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var username string
	err = tx.QueryRow(ctx, `UPDATE health_registry.tenant_provisioning_operations SET state=$2,error=NULLIF($3,''),updated_at=NOW() WHERE operation_id=$1 AND state=$4 RETURNING username`, operationID, to, strings.TrimSpace(operationError), from).Scan(&username)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrProvisioningStateConflict
	}
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE health_registry.users SET provisioning_state=$2, db_isolation_ready=CASE WHEN $2='active' THEN false ELSE db_isolation_ready END WHERE username=$1 AND provisioning_state=$3`, username, to, from)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrProvisioningStateConflict
	}
	if err := tx.Commit(ctx); err != nil {
		// Commit transport errors are ambiguous. Re-read before reporting a
		// conflict so callers never compensate for work that actually landed.
		var state ProvisioningState
		readCtx, cancel := detachedRegistryContext()
		defer cancel()
		if readErr := r.pool.QueryRow(readCtx, `SELECT state FROM health_registry.tenant_provisioning_operations WHERE operation_id=$1`, operationID).Scan(&state); readErr == nil && state == to {
			return nil
		}
		return err
	}
	return nil
}

// AdvanceProvisioning atomically keeps the user reservation and its durable
// operation in lockstep. It is used by startup reconciliation as well as setup.
func (r *Registry) AdvanceProvisioning(ctx context.Context, operationID uuid.UUID, from, to ProvisioningState, operationError string) error {
	if to == ProvisioningStateActive {
		return errors.New("activation requires exact provisioned tenant metadata")
	}
	return r.transitionUserAndOperation(ctx, operationID, from, to, operationError)
}

func (r *Registry) ActivateProvisioned(ctx context.Context, expected ProvisioningOperation, contract SchemaContractMetadata) error {
	if err := contract.Validate(); err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var username string
	err = tx.QueryRow(ctx, `UPDATE health_registry.tenant_provisioning_operations SET state='active',error=NULL,updated_at=NOW() WHERE operation_id=$1 AND tenant_id=$2 AND username=$3 AND schema_name=$4 AND db_role=$5 AND credential_version=$6 AND state='provisioning' RETURNING username`, expected.OperationID, expected.TenantID, expected.Username, expected.SchemaName, expected.DBRole, expected.CredentialVersion).Scan(&username)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrProvisioningStateConflict
	}
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE health_registry.users SET provisioning_state='active', db_isolation_ready=true, schema_contract_version=$6, schema_contract_checksum=$7 WHERE username=$1 AND tenant_id=$2 AND schema_name=$3 AND db_role=$4 AND db_credential_version=$5 AND provisioning_state='provisioning' AND schema_contract_version IS NULL AND schema_contract_checksum IS NULL`, username, expected.TenantID, expected.SchemaName, expected.DBRole, expected.CredentialVersion, contract.Version, contract.Checksum)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrProvisioningStateConflict
	}
	commit := r.commitProvisioningTx
	if commit == nil {
		commit = func(commitCtx context.Context, tx pgx.Tx) error { return tx.Commit(commitCtx) }
	}
	if err := commit(ctx, tx); err != nil {
		readCtx, cancel := detachedRegistryContext()
		defer cancel()
		landed, readErr := r.activateProvisionedLanded(readCtx, expected, contract)
		if readErr == nil && landed {
			return nil
		}
		return err
	}
	return nil
}

func (r *Registry) activateProvisionedLanded(ctx context.Context, expected ProvisioningOperation, contract SchemaContractMetadata) (bool, error) {
	var landed bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM health_registry.tenant_provisioning_operations AS op
			JOIN health_registry.users AS u ON u.username = op.username
			WHERE op.operation_id = $1
			  AND op.tenant_id = $2
			  AND op.username = $3
			  AND op.schema_name = $4
			  AND op.db_role = $5
			  AND op.credential_version = $6
			  AND op.state = 'active'
			  AND op.error IS NULL
			  AND u.username = $3
			  AND u.tenant_id = $2
			  AND u.schema_name = $4
			  AND u.db_role = $5
			  AND u.db_credential_version = $6
			  AND u.provisioning_state = 'active'
			  AND u.db_isolation_ready = true
			  AND u.schema_contract_version = $7
			  AND u.schema_contract_checksum = $8
		)
	`, expected.OperationID, expected.TenantID, expected.Username, expected.SchemaName, expected.DBRole, expected.CredentialVersion, contract.Version, contract.Checksum).Scan(&landed)
	if err != nil {
		return false, fmt.Errorf("verify provisioned activation commit: %w", err)
	}
	return landed, nil
}
