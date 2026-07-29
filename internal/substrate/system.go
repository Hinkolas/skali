package substrate

import (
	"context"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
)

// SystemClaimOutputs is what an internal consumer (the R6 object-storage
// metadata dependency) receives: connection identity plus the NAME of the
// credential Secret in skali-platform. The password itself stays in the
// Secret; consumers mount or read it themselves.
type SystemClaimOutputs struct {
	Provisioned      bool
	Waiting          string
	Host             string
	Port             int
	Database         string
	Username         string
	CredentialSecret string
}

// EnsureSystemClaim records an internal consumer's database claim and
// reports its outputs once provisioned. It traverses exactly the claim,
// placement, pool, tenant, credential, and observation paths user claims
// use; only policy differs (no environment, no output mirror, excluded from
// project release paths). Level-triggered: callers re-invoke until
// Provisioned, the substrate works toward it in the background.
func (c *Controller) EnsureSystemClaim(ctx context.Context, key string, spec dbstore.ClaimSpec) (SystemClaimOutputs, error) {
	row, err := c.deps.DB.EnsureClaim(ctx, dbstore.SystemOwner(key), spec)
	if err != nil {
		return SystemClaimOutputs{}, err
	}
	c.EnqueueClaim(row.ID)
	if claim.Phase(row.Phase) != claim.PhaseProvisioned {
		waiting := c.WaitingReason(row.ID)
		if waiting == "" {
			waiting = defaultWait(claim.Phase(row.Phase))
		}
		return SystemClaimOutputs{Waiting: waiting}, nil
	}
	tenant, err := c.deps.DB.LiveTenant(ctx, row.ID)
	if err != nil {
		return SystemClaimOutputs{}, err
	}
	return SystemClaimOutputs{
		Provisioned:      true,
		Host:             tenant.Host,
		Port:             int(tenant.Port),
		Database:         tenant.DatabaseName,
		Username:         tenant.RoleName,
		CredentialSecret: tenant.CredentialSecret,
	}, nil
}

// ReleaseSystemClaim starts teardown of an internal consumer's claim. It is
// deliberately separate from every project-scoped release path: system
// claims cannot be deleted through project operations.
func (c *Controller) ReleaseSystemClaim(ctx context.Context, key string) error {
	row, err := c.deps.DB.LiveSystemClaim(ctx, key)
	if err != nil {
		return err
	}
	if _, err := c.deps.DB.ReleaseClaim(ctx, row.ID); err != nil {
		return err
	}
	c.EnqueueClaim(row.ID)
	return nil
}
