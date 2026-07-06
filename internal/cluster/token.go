package cluster

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/store"
)

// Sentinel errors, mapped to HTTP codes in internal/api and to gRPC codes in
// the enrollment server.
var (
	ErrInvalidToken     = errors.New("cluster: invalid join token")
	ErrTokenUsed        = errors.New("cluster: join token already used or expired")
	ErrCAMismatch       = errors.New("cluster: master's CA does not match the token's fingerprint")
	ErrMasterNode       = errors.New("cluster: the master node cannot be modified this way")
	ErrNodeNotFound     = errors.New("cluster: node not found")
	ErrInvalidRole      = errors.New("cluster: invalid node role")
	ErrClusterAddrUnset = errors.New("cluster: CLUSTER_ADDR is not configured")
)

// joinTokenTTL bounds the window between minting a token in the UI and
// running `skalid enroll` on the new machine.
const joinTokenTTL = time.Hour

// AllRoles are the roles a node row may hold; AssignableRoles the subset an
// operator can grant — `master` is claimed by the master's own row at boot
// and never granted.
var (
	AllRoles        = []string{"master", "edge", "worker", "builder"}
	AssignableRoles = []string{"edge", "worker", "builder"}
)

// ValidateRoles checks a role set against allowed; empty or unknown roles are
// rejected with ErrInvalidRole.
func ValidateRoles(roles, allowed []string) error {
	if len(roles) == 0 {
		return fmt.Errorf("%w: at least one role required", ErrInvalidRole)
	}
	for _, r := range roles {
		if !slices.Contains(allowed, r) {
			return fmt.Errorf("%w: %q", ErrInvalidRole, r)
		}
	}
	return nil
}

// MintJoinToken creates a one-time enrollment secret. The returned token is
// `<id>.<secret>.<ca-fingerprint>`: the id locates the row, the secret proves
// possession (only its sha256 is stored), and the fingerprint pins the
// cluster CA so the enrolling node can authenticate the master before it
// trusts anything.
func MintJoinToken(ctx context.Context, st *store.Store, ca *CA, roles []string, createdBy *uuid.UUID) (token string, row store.JoinToken, err error) {
	if err := ValidateRoles(roles, AssignableRoles); err != nil {
		return "", store.JoinToken{}, err
	}

	id, err := uuid.NewV7()
	if err != nil {
		return "", store.JoinToken{}, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", store.JoinToken{}, err
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(secret))

	row, err = st.CreateJoinToken(ctx, store.CreateJoinTokenParams{
		ID:        id,
		Hash:      hash[:],
		Roles:     roles,
		ExpiresAt: time.Now().Add(joinTokenTTL),
		CreatedBy: createdBy,
	})
	if err != nil {
		return "", store.JoinToken{}, err
	}
	return fmt.Sprintf("%s.%s.%s", id, secret, Fingerprint(ca.Cert)), row, nil
}

// ParseJoinToken splits a rendered token into its parts. None of the segments
// can contain a dot (UUID, base64url, hex), so the format is unambiguous.
func ParseJoinToken(token string) (id uuid.UUID, secret, caFingerprint string, err error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return uuid.UUID{}, "", "", ErrInvalidToken
	}
	id, err = uuid.Parse(parts[0])
	if err != nil || parts[1] == "" || len(parts[2]) != 32 {
		return uuid.UUID{}, "", "", ErrInvalidToken
	}
	return id, parts[1], parts[2], nil
}

// hashJoinSecret is the at-rest form compared against join_tokens.hash.
func hashJoinSecret(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}
