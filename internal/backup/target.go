// Package backup owns manual environment backups and restores: the
// admin-configured external S3 targets, the snapshot layout and manifest
// format, and the controller that executes backup and restore runs. Durable
// snapshot history lives in the S3 manifests, never the control-plane
// database, because the primary use case is restoring after the cluster
// that held this database was destroyed.
package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/crypt"
	"github.com/Hinkolas/skali/internal/store"
)

// targetKeyInfo pins the HKDF domain separation for backup target secret
// keys; changing it invalidates every stored secret.
const targetKeyInfo = "skali/backup/target-key/v1"

// DefaultTargetName is the single target the v1 API manages. The table
// supports multiple named targets so later slices can add more without a
// migration.
const DefaultTargetName = "default"

var ErrTargetNotFound = errors.New("backup: target not configured")

// Target is a backup location without its secret access key. The secret is
// write-only: it never leaves the daemon after Upsert.
type Target struct {
	ID          uuid.UUID
	Name        string
	Endpoint    string
	Region      string
	Bucket      string
	Prefix      string
	AccessKeyID string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TargetInput carries one complete target write. Partial updates are not a
// thing: the CLI and web form always submit the full location.
type TargetInput struct {
	Name            string
	Endpoint        string
	Region          string
	Bucket          string
	Prefix          string
	AccessKeyID     string
	SecretAccessKey string
}

// TargetStore seals and serves backup targets. Reads never include the
// secret access key; the controller resolves credentials through
// credentials(), which decrypts in memory only.
type TargetStore struct {
	st  *store.Store
	key []byte
}

func NewTargetStore(st *store.Store, secret string) (*TargetStore, error) {
	if len(secret) < 32 {
		return nil, errors.New("backup: secret must be at least 32 bytes")
	}
	key, err := crypt.Key(secret, targetKeyInfo)
	if err != nil {
		return nil, fmt.Errorf("backup: derive key: %w", err)
	}
	return &TargetStore{st: st, key: key}, nil
}

func (s *TargetStore) Upsert(ctx context.Context, input TargetInput) (*Target, error) {
	if err := validateTargetInput(input); err != nil {
		return nil, err
	}
	ciphertext, err := crypt.Encrypt(s.key, []byte(input.SecretAccessKey))
	if err != nil {
		return nil, fmt.Errorf("backup: encrypt secret access key: %w", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("backup: generate target id: %w", err)
	}
	row, err := s.st.UpsertBackupTarget(ctx, store.UpsertBackupTargetParams{
		ID:              id,
		Name:            input.Name,
		Endpoint:        input.Endpoint,
		Region:          input.Region,
		Bucket:          input.Bucket,
		Prefix:          input.Prefix,
		AccessKeyID:     input.AccessKeyID,
		SecretAccessKey: ciphertext,
	})
	if err != nil {
		return nil, fmt.Errorf("backup: upsert target: %w", err)
	}
	return targetFromRow(row), nil
}

func (s *TargetStore) Get(ctx context.Context, name string) (*Target, error) {
	row, err := s.st.GetBackupTargetByName(ctx, name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTargetNotFound
		}
		return nil, fmt.Errorf("backup: get target: %w", err)
	}
	return targetFromRow(row), nil
}

// Credentials is the target plus its decrypted secret access key. It exists
// in memory only, for the controller and worker-secret rendering.
type Credentials struct {
	Target
	SecretAccessKey string
}

func (s *TargetStore) credentials(ctx context.Context, name string) (*Credentials, error) {
	row, err := s.st.GetBackupTargetByName(ctx, name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTargetNotFound
		}
		return nil, fmt.Errorf("backup: get target: %w", err)
	}
	plaintext, err := crypt.Decrypt(s.key, row.SecretAccessKey)
	if err != nil {
		return nil, fmt.Errorf("backup: decrypt secret access key: %w", err)
	}
	return &Credentials{
		Target:          *targetFromRow(row),
		SecretAccessKey: string(plaintext),
	}, nil
}

func targetFromRow(row store.BackupTarget) *Target {
	return &Target{
		ID:          row.ID,
		Name:        row.Name,
		Endpoint:    row.Endpoint,
		Region:      row.Region,
		Bucket:      row.Bucket,
		Prefix:      row.Prefix,
		AccessKeyID: row.AccessKeyID,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}

// ValidationError reports an invalid target field by name so the API can
// answer 422 with a useful message.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("backup: invalid target %s: %s", e.Field, e.Message)
}

func validateTargetInput(input TargetInput) error {
	if input.Name == "" {
		return &ValidationError{Field: "name", Message: "must not be empty"}
	}
	if !strings.HasPrefix(input.Endpoint, "http://") && !strings.HasPrefix(input.Endpoint, "https://") {
		return &ValidationError{Field: "endpoint", Message: "must be an http or https URL"}
	}
	if input.Bucket == "" {
		return &ValidationError{Field: "bucket", Message: "must not be empty"}
	}
	if input.AccessKeyID == "" {
		return &ValidationError{Field: "access_key_id", Message: "must not be empty"}
	}
	if input.SecretAccessKey == "" {
		return &ValidationError{Field: "secret_access_key", Message: "must not be empty"}
	}
	return nil
}
