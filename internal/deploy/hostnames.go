package deploy

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/edge"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// HostnameConflict exposes only the candidate field, never the other tenant.
type HostnameConflict struct {
	Field    string
	Reserved bool
}

func (e *HostnameConflict) Error() string {
	if e.Reserved {
		return e.Field + ": hostname is reserved for the installation"
	}
	return e.Field + ": hostname is already claimed by another environment"
}

func (s *Service) ReserveHostnames(ctx context.Context, hosts []string) error {
	canonical := map[string]bool{}
	for _, host := range hosts {
		if host == "" {
			continue
		}
		name, err := edge.CanonicalDomain(host)
		if err != nil {
			return fmt.Errorf("reserved hostname: %w", err)
		}
		canonical[name] = true
	}
	names := make([]string, 0, len(canonical))
	for host := range canonical {
		names = append(names, host)
	}
	sort.Strings(names)
	return s.st.WithTx(ctx, func(q *store.Queries) error {
		for _, name := range names {
			n, err := q.ReserveHostname(ctx, name)
			if err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("reserved hostname overlaps an environment claim; configuration was not applied")
			}
		}
		return nil
	})
}

func (s *Service) revisionRoutes(ctx context.Context, env uuid.UUID, rev *revision.Revision) ([]compiler.ResolvedRoute, error) {
	refs := map[string]int{}
	for key, ref := range rev.Secrets {
		refs[key] = ref.Version
	}
	variables, err := s.values.Plaintexts(ctx, env, refs)
	if err != nil {
		return nil, err
	}
	return compiler.ResolveRoutes(rev.Definition, variables)
}

func (s *Service) checkRoutes(ctx context.Context, env uuid.UUID, rev *revision.Revision) error {
	routes, err := s.revisionRoutes(ctx, env, rev)
	if err != nil {
		return err
	}
	for _, route := range routes {
		claim, err := s.st.GetHostnameClaim(ctx, route.Domain)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if claim.Reserved || claim.EnvironmentID == nil || *claim.EnvironmentID != env {
			return &HostnameConflict{route.Field, claim.Reserved}
		}
	}
	return nil
}

func (s *Service) claimRoutesTx(ctx context.Context, q *store.Queries, env, revisionID uuid.UUID, routes []compiler.ResolvedRoute) error {
	if err := q.RetireEnvironmentHostnames(ctx, &env); err != nil {
		return err
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Domain < routes[j].Domain })
	for _, route := range routes {
		n, err := q.AcquireHostname(ctx, store.AcquireHostnameParams{Hostname: route.Domain, EnvironmentID: &env, TargetRevisionID: &revisionID})
		if err != nil {
			return err
		}
		if n == 0 {
			claim, err := q.GetHostnameClaim(ctx, route.Domain)
			if err != nil {
				return err
			}
			return &HostnameConflict{route.Field, claim.Reserved}
		}
	}
	return nil
}

// ReleaseAbsentHostnames runs under the environment lock after a fresh cluster
// read. Retired claims remain durable until their final router is gone.
func (s *Service) ReleaseAbsentHostnames(ctx context.Context, env uuid.UUID, live map[string]bool) error {
	target, err := s.st.GetEnvironmentTarget(ctx, env)
	if err != nil {
		return err
	}
	return s.st.WithTx(ctx, func(q *store.Queries) error {
		if target.State != EnvironmentStateActive || target.TargetRevisionID == nil {
			if err := q.RetireEnvironmentHostnames(ctx, &env); err != nil {
				return err
			}
		}
		claims, err := q.ListEnvironmentHostnames(ctx, &env)
		if err != nil {
			return err
		}
		for _, claim := range claims {
			if claim.TargetRevisionID == nil && !live[claim.Hostname] {
				if err := q.DeleteRetiredHostname(ctx, store.DeleteRetiredHostnameParams{Hostname: claim.Hostname, EnvironmentID: &env}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// ResumeRevision is the restore controller's target transition. It has the
// same claim transaction as deployment without creating a second run.
func (s *Service) ResumeRevision(ctx context.Context, env, id uuid.UUID) error {
	unlock, err := s.st.LockEnvironment(ctx, env)
	if err != nil {
		return err
	}
	defer unlock()
	row, err := s.st.GetRevisionByID(ctx, id)
	if err != nil {
		return err
	}
	if row.EnvironmentID != env {
		return ErrRevisionMismatch
	}
	rev, err := s.GetRevision(ctx, id)
	if err != nil {
		return err
	}
	routes, err := s.revisionRoutes(ctx, env, rev)
	if err != nil {
		return err
	}
	return s.st.WithTx(ctx, func(q *store.Queries) error {
		if err := s.claimRoutesTx(ctx, q, env, id, routes); err != nil {
			return err
		}
		n, err := q.SetEnvironmentTarget(ctx, store.SetEnvironmentTargetParams{EnvironmentID: env, TargetRevisionID: &id})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrEnvironmentReleasing
		}
		return nil
	})
}

func (s *Service) FallbackTarget(ctx context.Context, in store.FallbackEnvironmentTargetParams) (int64, error) {
	unlock, err := s.st.LockEnvironment(ctx, in.EnvironmentID)
	if err != nil {
		return 0, err
	}
	defer unlock()
	return s.FallbackTargetLocked(ctx, in)
}

// FallbackTargetLocked is used by reconciliation, which already holds the
// environment lock for its entire pass. Even automatic recovery must reclaim
// a hostname which may have been released after the old route disappeared.
func (s *Service) FallbackTargetLocked(ctx context.Context, in store.FallbackEnvironmentTargetParams) (int64, error) {
	target, err := s.st.GetEnvironmentTarget(ctx, in.EnvironmentID)
	if err != nil {
		return 0, err
	}
	if target.ActiveRevisionID == nil {
		return 0, nil
	}
	rev, err := s.GetRevision(ctx, *target.ActiveRevisionID)
	if err != nil {
		return 0, err
	}
	routes, err := s.revisionRoutes(ctx, in.EnvironmentID, rev)
	if err != nil {
		return 0, err
	}
	var n int64
	err = s.st.WithTx(ctx, func(q *store.Queries) error {
		var err error
		n, err = q.FallbackEnvironmentTarget(ctx, in)
		if err != nil || n == 0 {
			return err
		}
		return s.claimRoutesTx(ctx, q, in.EnvironmentID, *target.ActiveRevisionID, routes)
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}
