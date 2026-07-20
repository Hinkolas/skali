// Package valuestore owns the typed environment value store: plain values in
// plaintext, secret values encrypted at rest, both as append-only per-name
// versions. Candidate staging and promotion implement the deployment
// contract: a failed preparation discards staged rows and never touches the
// current values, while promotion flips a whole candidate batch to current
// inside the caller's transaction.
package valuestore

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Hinkolas/skali/internal/crypt"
	"github.com/Hinkolas/skali/internal/redact"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/values"
)

// keyInfo pins the HKDF domain separation for secret values; changing it
// invalidates every stored secret.
const keyInfo = "skali/values/secret-key/v1"

var (
	ErrEnvironmentNotFound = errors.New("valuestore: environment not found")
	// ErrStagingConflict: a concurrent staging allocated the same version for
	// one of the names; the caller retries with a fresh candidate.
	ErrStagingConflict = errors.New("valuestore: concurrent staging conflict")
)

type Service struct {
	st  *store.Store
	key []byte
}

func New(st *store.Store, secret string) (*Service, error) {
	if len(secret) < 32 {
		return nil, errors.New("valuestore: secret must be at least 32 bytes")
	}
	key, err := crypt.Key(secret, keyInfo)
	if err != nil {
		return nil, fmt.Errorf("valuestore: derive key: %w", err)
	}
	return &Service{st: st, key: key}, nil
}

// Candidate describes one staged batch. Secret plaintexts are never echoed;
// only names and allocated versions leave the staging transaction.
type Candidate struct {
	ID             uuid.UUID
	Plain          []string
	Secret         []string
	SecretVersions map[string]int64
}

// Stage inserts the resolved values as one staged batch. Nothing current
// changes; promotion or discard decides the batch's fate.
func (s *Service) Stage(ctx context.Context, environmentID uuid.UUID, resolved values.Resolved) (*Candidate, error) {
	if err := s.environmentExists(ctx, environmentID); err != nil {
		return nil, err
	}
	candidateID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("valuestore: generate candidate id: %w", err)
	}
	candidate := &Candidate{
		ID:             candidateID,
		SecretVersions: make(map[string]int64, len(resolved.Secret)),
	}
	err = s.st.WithTx(ctx, func(q *store.Queries) error {
		for _, name := range sortedKeys(resolved.Plain) {
			id, err := uuid.NewV7()
			if err != nil {
				return fmt.Errorf("valuestore: generate id: %w", err)
			}
			if _, err := q.StageEnvironmentValue(ctx, store.StageEnvironmentValueParams{
				ID:            id,
				EnvironmentID: environmentID,
				Name:          name,
				Value:         resolved.Plain[name],
				CandidateID:   &candidateID,
			}); err != nil {
				return stagingError(err)
			}
			candidate.Plain = append(candidate.Plain, name)
		}
		for _, name := range sortedKeys(resolved.Secret) {
			ciphertext, err := crypt.Encrypt(s.key, []byte(resolved.Secret[name]))
			if err != nil {
				return fmt.Errorf("valuestore: encrypt %s: %w", name, err)
			}
			id, err := uuid.NewV7()
			if err != nil {
				return fmt.Errorf("valuestore: generate id: %w", err)
			}
			row, err := q.StageEnvironmentSecret(ctx, store.StageEnvironmentSecretParams{
				ID:            id,
				EnvironmentID: environmentID,
				Name:          name,
				Ciphertext:    ciphertext,
				CandidateID:   &candidateID,
			})
			if err != nil {
				return stagingError(err)
			}
			candidate.Secret = append(candidate.Secret, name)
			candidate.SecretVersions[name] = row.Version
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return candidate, nil
}

func (s *Service) CurrentPlain(ctx context.Context, environmentID uuid.UUID) (map[string]string, error) {
	rows, err := s.st.ListCurrentEnvironmentValues(ctx, environmentID)
	if err != nil {
		return nil, fmt.Errorf("valuestore: list current values: %w", err)
	}
	plain := make(map[string]string, len(rows))
	for _, row := range rows {
		plain[row.Name] = row.Value
	}
	return plain, nil
}

func (s *Service) CurrentSecretVersions(ctx context.Context, environmentID uuid.UUID) (map[string]int64, error) {
	rows, err := s.st.ListCurrentEnvironmentSecretVersions(ctx, environmentID)
	if err != nil {
		return nil, fmt.Errorf("valuestore: list current secret versions: %w", err)
	}
	versions := make(map[string]int64, len(rows))
	for _, row := range rows {
		versions[row.Name] = row.Version
	}
	return versions, nil
}

// StagedPlain returns the staged plain values of one candidate batch.
func (s *Service) StagedPlain(ctx context.Context, environmentID, candidateID uuid.UUID) (map[string]string, error) {
	rows, err := s.st.ListStagedEnvironmentValues(ctx, store.ListStagedEnvironmentValuesParams{
		EnvironmentID: environmentID,
		CandidateID:   &candidateID,
	})
	if err != nil {
		return nil, fmt.Errorf("valuestore: list staged values: %w", err)
	}
	plain := make(map[string]string, len(rows))
	for _, row := range rows {
		plain[row.Name] = row.Value
	}
	return plain, nil
}

// StagedSecretVersions returns the staged secret versions of one candidate.
func (s *Service) StagedSecretVersions(ctx context.Context, environmentID, candidateID uuid.UUID) (map[string]int64, error) {
	rows, err := s.st.ListStagedEnvironmentSecretVersions(ctx, store.ListStagedEnvironmentSecretVersionsParams{
		EnvironmentID: environmentID,
		CandidateID:   &candidateID,
	})
	if err != nil {
		return nil, fmt.Errorf("valuestore: list staged secret versions: %w", err)
	}
	versions := make(map[string]int64, len(rows))
	for _, row := range rows {
		versions[row.Name] = row.Version
	}
	return versions, nil
}

// Entry is one line of the values summary. Value is set for plain names
// only; secret values never leave the store in a summary.
type Entry struct {
	Name    string
	Secret  bool
	Version int64
	Value   string
}

// Summary lists the current values of an environment, plain and secret,
// sorted by name.
func (s *Service) Summary(ctx context.Context, environmentID uuid.UUID) ([]Entry, error) {
	if err := s.environmentExists(ctx, environmentID); err != nil {
		return nil, err
	}
	plainRows, err := s.st.ListCurrentEnvironmentValues(ctx, environmentID)
	if err != nil {
		return nil, fmt.Errorf("valuestore: list current values: %w", err)
	}
	secretRows, err := s.st.ListCurrentEnvironmentSecretVersions(ctx, environmentID)
	if err != nil {
		return nil, fmt.Errorf("valuestore: list current secret versions: %w", err)
	}
	entries := make([]Entry, 0, len(plainRows)+len(secretRows))
	for _, row := range plainRows {
		entries = append(entries, Entry{Name: row.Name, Version: row.Version, Value: row.Value})
	}
	for _, row := range secretRows {
		entries = append(entries, Entry{Name: row.Name, Secret: true, Version: row.Version})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// Redactor decrypts the environment's current secrets, plus the staged
// secrets of candidateID when it is not uuid.Nil, in memory only, and
// returns the matcher the journal writer uses. The map is keyed by
// plaintext, so a current and a staged version of the same name are both
// redacted.
func (s *Service) Redactor(ctx context.Context, environmentID, candidateID uuid.UUID) (*redact.Redactor, error) {
	byPlaintext := make(map[string]string)
	current, err := s.st.ListCurrentEnvironmentSecretCiphertexts(ctx, environmentID)
	if err != nil {
		return nil, fmt.Errorf("valuestore: list current secrets: %w", err)
	}
	for _, row := range current {
		plaintext, err := crypt.Decrypt(s.key, row.Ciphertext)
		if err != nil {
			return nil, fmt.Errorf("valuestore: decrypt %s: %w", row.Name, err)
		}
		byPlaintext[string(plaintext)] = row.Name
	}
	if candidateID != uuid.Nil {
		staged, err := s.st.ListStagedEnvironmentSecretCiphertexts(ctx, store.ListStagedEnvironmentSecretCiphertextsParams{
			EnvironmentID: environmentID,
			CandidateID:   &candidateID,
		})
		if err != nil {
			return nil, fmt.Errorf("valuestore: list staged secrets: %w", err)
		}
		for _, row := range staged {
			plaintext, err := crypt.Decrypt(s.key, row.Ciphertext)
			if err != nil {
				return nil, fmt.Errorf("valuestore: decrypt %s: %w", row.Name, err)
			}
			byPlaintext[string(plaintext)] = row.Name
		}
	}
	return redact.New(byPlaintext), nil
}

// SecretPlaintexts decrypts the exact secret versions a revision pinned.
// Superseded rows are retained by the state model precisely so old pinned
// versions keep resolving. Results live only in memory and in the applied
// cluster Secret; callers must never log or persist them.
func (s *Service) SecretPlaintexts(ctx context.Context, environmentID uuid.UUID, refs map[string]int) (map[string]string, error) {
	plaintexts := make(map[string]string, len(refs))
	for _, name := range sortedRefKeys(refs) {
		ciphertext, err := s.st.GetEnvironmentSecretCiphertext(ctx, store.GetEnvironmentSecretCiphertextParams{
			EnvironmentID: environmentID,
			Name:          name,
			Version:       int64(refs[name]),
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("valuestore: secret %s version %d not found", name, refs[name])
			}
			return nil, fmt.Errorf("valuestore: get secret %s: %w", name, err)
		}
		plaintext, err := crypt.Decrypt(s.key, ciphertext)
		if err != nil {
			return nil, fmt.Errorf("valuestore: decrypt %s: %w", name, err)
		}
		plaintexts[name] = string(plaintext)
	}
	return plaintexts, nil
}

// DiscardCandidate deletes the staged rows of one batch. The queries never
// select the value or ciphertext columns.
func (s *Service) DiscardCandidate(ctx context.Context, environmentID, candidateID uuid.UUID) error {
	return s.st.WithTx(ctx, func(q *store.Queries) error {
		if _, err := q.DiscardStagedEnvironmentValues(ctx, store.DiscardStagedEnvironmentValuesParams{
			EnvironmentID: environmentID,
			CandidateID:   &candidateID,
		}); err != nil {
			return fmt.Errorf("valuestore: discard staged values: %w", err)
		}
		if _, err := q.DiscardStagedEnvironmentSecrets(ctx, store.DiscardStagedEnvironmentSecretsParams{
			EnvironmentID: environmentID,
			CandidateID:   &candidateID,
		}); err != nil {
			return fmt.Errorf("valuestore: discard staged secrets: %w", err)
		}
		return nil
	})
}

// SweepStaged deletes staged rows older than age; the safety net behind
// explicit discards. Runs at boot and periodically.
func (s *Service) SweepStaged(ctx context.Context, age time.Duration) (int64, error) {
	cutoff := time.Now().Add(-age)
	var total int64
	err := s.st.WithTx(ctx, func(q *store.Queries) error {
		rows, err := q.SweepStagedEnvironmentValues(ctx, cutoff)
		if err != nil {
			return fmt.Errorf("valuestore: sweep staged values: %w", err)
		}
		total += rows
		rows, err = q.SweepStagedEnvironmentSecrets(ctx, cutoff)
		if err != nil {
			return fmt.Errorf("valuestore: sweep staged secrets: %w", err)
		}
		total += rows
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

// PromoteTx flips the whole candidate batch to current inside the caller's
// transaction: prior current rows for the staged names become superseded,
// then the staged rows become current. Composes into the deploy promotion
// so target movement and value promotion are atomic.
func (s *Service) PromoteTx(ctx context.Context, q *store.Queries, environmentID, candidateID uuid.UUID) error {
	if candidateID == uuid.Nil {
		return nil
	}
	if err := q.SupersedeCurrentEnvironmentValues(ctx, store.SupersedeCurrentEnvironmentValuesParams{
		EnvironmentID: environmentID,
		CandidateID:   &candidateID,
	}); err != nil {
		return fmt.Errorf("valuestore: supersede values: %w", err)
	}
	if _, err := q.PromoteStagedEnvironmentValues(ctx, store.PromoteStagedEnvironmentValuesParams{
		EnvironmentID: environmentID,
		CandidateID:   &candidateID,
	}); err != nil {
		return fmt.Errorf("valuestore: promote values: %w", err)
	}
	if err := q.SupersedeCurrentEnvironmentSecrets(ctx, store.SupersedeCurrentEnvironmentSecretsParams{
		EnvironmentID: environmentID,
		CandidateID:   &candidateID,
	}); err != nil {
		return fmt.Errorf("valuestore: supersede secrets: %w", err)
	}
	if _, err := q.PromoteStagedEnvironmentSecrets(ctx, store.PromoteStagedEnvironmentSecretsParams{
		EnvironmentID: environmentID,
		CandidateID:   &candidateID,
	}); err != nil {
		return fmt.Errorf("valuestore: promote secrets: %w", err)
	}
	return nil
}

func (s *Service) environmentExists(ctx context.Context, environmentID uuid.UUID) error {
	if _, err := s.st.GetEnvironmentByID(ctx, environmentID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrEnvironmentNotFound
		}
		return fmt.Errorf("valuestore: get environment: %w", err)
	}
	return nil
}

func stagingError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
		return ErrStagingConflict
	}
	return fmt.Errorf("valuestore: stage: %w", err)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedRefKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
