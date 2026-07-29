// Package dbstore persists the shared database substrate's durable state:
// claims, physical clusters, placements, and tenants (REWORK_V2 sections
// 10.2 and 10.3). Every claim phase change is guarded by the internal/claim
// machine and every cluster state change by ClusterStates, both under row
// locks. User and system claims are one table and one code path; owner kind
// is data, never a branch. Credential values never pass through this
// package: rows carry Secret names and versions only.
package dbstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/lifecycle"
	"github.com/Hinkolas/skali/internal/store"
)

var (
	ErrNotFound = errors.New("dbstore: not found")
	// ErrInvalidTransition: the requested phase or state change is not
	// permitted by the corresponding lifecycle machine, or a concurrent
	// writer got there first.
	ErrInvalidTransition = errors.New("dbstore: invalid transition")
	// ErrSpecConflict: the desired spec changes engine, major, or isolation,
	// which identify the claim's physical home. That is a destructive
	// replacement decided at plan level, never an in-place update.
	ErrSpecConflict = errors.New("dbstore: claim spec conflicts with its live claim")
)

// Owner kinds and cluster classes/states are stored as text; these constants
// are the vocabulary.
const (
	OwnerService = "service"
	OwnerSystem  = "system"

	ClassShared      = "shared"
	ClassEnvironment = "environment"
	ClassDedicated   = "dedicated"

	StateActive     = "active"
	StateHibernated = "hibernated"
	StateReleasing  = "releasing"
	StateReleased   = "released"
)

// ClusterStates is the pool lifecycle machine. Hibernated is the local-dev
// idle state (workloads stopped, data kept); production pools move straight
// between active and releasing.
var ClusterStates = lifecycle.Machine[string]{
	States: []string{StateActive, StateHibernated, StateReleasing, StateReleased},
	Transitions: map[string][]string{
		StateActive:     {StateHibernated, StateReleasing},
		StateHibernated: {StateActive, StateReleasing},
		StateReleasing:  {StateReleased},
	},
}

// Owner identifies who a claim belongs to. Ref is the denormalized
// human-readable identity retained for history after the owning rows are
// gone.
type Owner struct {
	Kind          string
	ProjectID     uuid.UUID
	EnvironmentID uuid.UUID
	ServiceKey    string
	SystemKey     string
	Ref           string
}

// ServiceOwner identifies a project service's claim. Names feed the durable
// ref; IDs feed the live lookups.
func ServiceOwner(projectID, environmentID uuid.UUID, projectName, environmentName, serviceKey string) Owner {
	return Owner{
		Kind:          OwnerService,
		ProjectID:     projectID,
		EnvironmentID: environmentID,
		ServiceKey:    serviceKey,
		Ref: fmt.Sprintf("project/%s/environment/%s/service/%s",
			projectName, environmentName, serviceKey),
	}
}

// SystemOwner identifies an internal consumer's claim, e.g.
// "object-storage/metadata".
func SystemOwner(key string) Owner {
	return Owner{Kind: OwnerSystem, SystemKey: key, Ref: "system/" + key}
}

// ClaimSpec is the desired database capability. Engine, Major, and Isolation
// identify the claim's physical home and are immutable on a live claim; the
// remaining fields may drift and fold into it.
type ClaimSpec struct {
	Engine       string
	Major        int
	Isolation    string
	Availability string
	StorageBytes int64
	Extensions   []string
	PITRSeconds  int64
}

type Service struct {
	st *store.Store
}

func New(st *store.Store) *Service {
	return &Service{st: st}
}

func marshalExtensions(extensions []string) []byte {
	sorted := slices.Clone(extensions)
	slices.Sort(sorted)
	if len(sorted) == 0 {
		return []byte("[]")
	}
	data, err := json.Marshal(sorted)
	if err != nil {
		// A []string cannot fail to marshal; keep the invariant visible.
		panic(fmt.Sprintf("dbstore: marshal extensions: %v", err))
	}
	return data
}

// Extensions decodes a claim row's extension list.
func Extensions(row store.DatabaseClaim) []string {
	var extensions []string
	if err := json.Unmarshal(row.Extensions, &extensions); err != nil {
		return nil
	}
	return extensions
}

func optionalID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}
