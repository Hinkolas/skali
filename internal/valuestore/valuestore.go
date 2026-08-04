// Package valuestore owns the environment value store. Every value is a
// secret: encrypted at rest, write-only through the API, stored as
// append-only per-name versions. Candidate staging and promotion implement
// the deployment contract: a failed preparation discards staged rows and
// never touches the current values, while promotion flips a whole candidate
// batch to current inside the caller's transaction. Unset tombstones the
// current generation without a successor, so pinned old revisions keep
// resolving.
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

// Candidate describes one staged batch. Plaintexts are never echoed; only
// names and allocated versions leave the staging transaction.
type Candidate struct {
	ID       uuid.UUID
	Names    []string
	Versions map[string]int64
}

// Stage encrypts and inserts the provided values as one staged batch. An
// empty string is a real value and stages like any other. Nothing current
// changes; promotion or discard decides the batch's fate. An empty map still
// allocates a candidate so deployments can carry an empty batch.
func (s *Service) Stage(ctx context.Context, environmentID uuid.UUID, provided map[string]string) (*Candidate, error) {
	if err := s.environmentExists(ctx, environmentID); err != nil {
		return nil, err
	}
	candidateID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("valuestore: generate candidate id: %w", err)
	}
	candidate := &Candidate{
		ID:       candidateID,
		Versions: make(map[string]int64, len(provided)),
	}
	err = s.st.WithTx(ctx, func(q *store.Queries) error {
		for _, name := range sortedKeys(provided) {
			ciphertext, err := crypt.Encrypt(s.key, []byte(provided[name]))
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
			candidate.Names = append(candidate.Names, name)
			candidate.Versions[name] = row.Version
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return candidate, nil
}

// CurrentVersions returns the current generation of every set value.
func (s *Service) CurrentVersions(ctx context.Context, environmentID uuid.UUID) (map[string]int64, error) {
	rows, err := s.st.ListCurrentEnvironmentSecretVersions(ctx, environmentID)
	if err != nil {
		return nil, fmt.Errorf("valuestore: list current versions: %w", err)
	}
	versions := make(map[string]int64, len(rows))
	for _, row := range rows {
		versions[row.Name] = row.Version
	}
	return versions, nil
}

// StagedVersions returns the staged versions of one candidate batch.
func (s *Service) StagedVersions(ctx context.Context, environmentID, candidateID uuid.UUID) (map[string]int64, error) {
	rows, err := s.st.ListStagedEnvironmentSecretVersions(ctx, store.ListStagedEnvironmentSecretVersionsParams{
		EnvironmentID: environmentID,
		CandidateID:   &candidateID,
	})
	if err != nil {
		return nil, fmt.Errorf("valuestore: list staged versions: %w", err)
	}
	versions := make(map[string]int64, len(rows))
	for _, row := range rows {
		versions[row.Name] = row.Version
	}
	return versions, nil
}

// Entry is one line of the values summary. Values are write-only: an entry
// carries the name and version alone, never a plaintext.
type Entry struct {
	Name    string
	Version int64
}

// Summary lists the current values of an environment, sorted by name.
func (s *Service) Summary(ctx context.Context, environmentID uuid.UUID) ([]Entry, error) {
	if err := s.environmentExists(ctx, environmentID); err != nil {
		return nil, err
	}
	rows, err := s.st.ListCurrentEnvironmentSecretVersions(ctx, environmentID)
	if err != nil {
		return nil, fmt.Errorf("valuestore: list current versions: %w", err)
	}
	entries := make([]Entry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, Entry{Name: row.Name, Version: row.Version})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// Unset supersedes the current generation of each name without a successor:
// the value disappears from future revisions while pinned (name, version)
// resolution for existing revisions keeps working. A candidate staged before
// Unset re-creates the name as current when it promotes. Returns the number
// of names actually unset.
func (s *Service) Unset(ctx context.Context, environmentID uuid.UUID, names []string) (int64, error) {
	if err := s.environmentExists(ctx, environmentID); err != nil {
		return 0, err
	}
	rows, err := s.st.UnsetCurrentEnvironmentSecrets(ctx, store.UnsetCurrentEnvironmentSecretsParams{
		EnvironmentID: environmentID,
		Names:         names,
	})
	if err != nil {
		return 0, fmt.Errorf("valuestore: unset values: %w", err)
	}
	return rows, nil
}

// Redactor decrypts the environment's current values, plus the staged batch
// of candidateID when it is not uuid.Nil, in memory only, and returns the
// matcher the journal writer uses. The map is keyed by plaintext, so a
// current and a staged version of the same name are both redacted.
func (s *Service) Redactor(ctx context.Context, environmentID, candidateID uuid.UUID) (*redact.Redactor, error) {
	byPlaintext := make(map[string]string)
	current, err := s.st.ListCurrentEnvironmentSecretCiphertexts(ctx, environmentID)
	if err != nil {
		return nil, fmt.Errorf("valuestore: list current values: %w", err)
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
			return nil, fmt.Errorf("valuestore: list staged values: %w", err)
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

// Plaintexts decrypts the exact versions a revision pinned. Superseded rows
// are retained by the state model precisely so old pinned versions keep
// resolving, including tombstoned names. Results live only in memory and in
// the applied cluster Secret; callers must never log or persist them.
func (s *Service) Plaintexts(ctx context.Context, environmentID uuid.UUID, refs map[string]int) (map[string]string, error) {
	plaintexts := make(map[string]string, len(refs))
	for _, name := range sortedRefKeys(refs) {
		ciphertext, err := s.st.GetEnvironmentSecretCiphertext(ctx, store.GetEnvironmentSecretCiphertextParams{
			EnvironmentID: environmentID,
			Name:          name,
			Version:       int64(refs[name]),
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("valuestore: value %s version %d not found", name, refs[name])
			}
			return nil, fmt.Errorf("valuestore: get value %s: %w", name, err)
		}
		plaintext, err := crypt.Decrypt(s.key, ciphertext)
		if err != nil {
			return nil, fmt.Errorf("valuestore: decrypt %s: %w", name, err)
		}
		plaintexts[name] = string(plaintext)
	}
	return plaintexts, nil
}

// DiscardCandidate deletes the staged rows of one batch. The query never
// selects the ciphertext column.
func (s *Service) DiscardCandidate(ctx context.Context, environmentID, candidateID uuid.UUID) error {
	if _, err := s.st.DiscardStagedEnvironmentSecrets(ctx, store.DiscardStagedEnvironmentSecretsParams{
		EnvironmentID: environmentID,
		CandidateID:   &candidateID,
	}); err != nil {
		return fmt.Errorf("valuestore: discard staged values: %w", err)
	}
	return nil
}

// SweepStaged deletes staged rows older than age; the safety net behind
// explicit discards. Runs at boot and periodically.
func (s *Service) SweepStaged(ctx context.Context, age time.Duration) (int64, error) {
	cutoff := time.Now().Add(-age)
	rows, err := s.st.SweepStagedEnvironmentSecrets(ctx, cutoff)
	if err != nil {
		return 0, fmt.Errorf("valuestore: sweep staged values: %w", err)
	}
	return rows, nil
}

// PromoteTx flips the whole candidate batch to current inside the caller's
// transaction: prior current rows for the staged names become superseded,
// then the staged rows become current. Composes into the deploy promotion
// so target movement and value promotion are atomic.
func (s *Service) PromoteTx(ctx context.Context, q *store.Queries, environmentID, candidateID uuid.UUID) error {
	if candidateID == uuid.Nil {
		return nil
	}
	if err := q.SupersedeCurrentEnvironmentSecrets(ctx, store.SupersedeCurrentEnvironmentSecretsParams{
		EnvironmentID: environmentID,
		CandidateID:   &candidateID,
	}); err != nil {
		return fmt.Errorf("valuestore: supersede values: %w", err)
	}
	if _, err := q.PromoteStagedEnvironmentSecrets(ctx, store.PromoteStagedEnvironmentSecretsParams{
		EnvironmentID: environmentID,
		CandidateID:   &candidateID,
	}); err != nil {
		return fmt.Errorf("valuestore: promote values: %w", err)
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
