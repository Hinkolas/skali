package deploy

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/utils"
)

// TransitionError: the candidate changes a property that fixes where a
// live database or bucket keeps its data (a database's engine, version,
// or isolation; a bucket's visibility or versioning). The live claim
// cannot apply it in place, and confirming destructive changes does not
// make it supported: it would mean a new, empty service under the old
// name. Nothing is built, promoted, or touched.
type TransitionError struct {
	// Service is the dotted service name ("databases.main").
	Service string
	Field   string
	From    string
	To      string
}

func (e *TransitionError) Error() string {
	kind, data := "database", "data"
	if strings.HasPrefix(e.Service, "buckets.") {
		kind, data = "bucket", "objects"
	}
	return fmt.Sprintf("deploy: %s.%s cannot change from %s to %s on an existing %s; its %s cannot move in place. "+
		"Keep %s %s, or declare a new %s with %s %s, copy the %s across, and remove the old one",
		e.Service, e.Field, e.From, e.To, kind, data,
		e.Field, e.From, kind, e.Field, e.To, data)
}

// BucketQuotaError: the candidate lowers a bucket's storage quota to or
// below what the bucket stores now. Uploads stop once usage reaches the
// quota, so the bucket would turn read-only as soon as it applied.
type BucketQuotaError struct {
	// Bucket is the dotted service name ("buckets.media").
	Bucket     string
	UsedBytes  int64
	QuotaBytes int64
}

func (e *BucketQuotaError) Error() string {
	return fmt.Sprintf("deploy: %s.quotas.storage cannot drop to %s: the bucket stores %s, and uploads stop once usage reaches the quota. "+
		"Delete objects until it stores less than %s, or choose a quota above %s",
		e.Bucket, utils.FormatBytes(e.QuotaBytes), utils.FormatBytes(e.UsedBytes),
		utils.FormatBytes(e.QuotaBytes), utils.FormatBytes(e.UsedBytes))
}

// BucketUsageUnavailableError: a quota reduction cannot be checked
// because the bucket's usage could not be measured. It is never guessed.
type BucketUsageUnavailableError struct {
	Bucket     string
	QuotaBytes int64
	Err        error
}

func (e *BucketUsageUnavailableError) Error() string {
	return fmt.Sprintf("deploy: %s.quotas.storage cannot drop to %s: the bucket's usage could not be measured (%v). "+
		"Retry once object storage is reachable, or keep the current quota",
		e.Bucket, utils.FormatBytes(e.QuotaBytes), e.Err)
}

func (e *BucketUsageUnavailableError) Unwrap() error { return e.Err }

// BucketUsage measures what a live bucket claim stores right now: its
// live bytes as the object store's master reports them.
type BucketUsage interface {
	BucketUsedBytes(ctx context.Context, claimID uuid.UUID) (int64, error)
}

// bucketUsageTimeout bounds one measurement on the request path.
const bucketUsageTimeout = 15 * time.Second

// errNoBucketUsage: this daemon has no object store to measure (it runs
// without a cluster connection).
var errNoBucketUsage = errors.New("this daemon has no object store connection")

// SetBucketUsage wires the measurement quota reductions are checked
// against. Without one, every reduction is refused as unmeasurable.
func (s *Service) SetBucketUsage(usage BucketUsage) { s.bucketUsage = usage }

// checkTransitions compares a definition with the environment's live
// claims, which reflect what actually exists after failed deployments and
// fallbacks rather than what the active revision says. It refuses changes
// a live claim cannot apply in place and quota reductions the bucket's
// current usage does not fit under. Deploy preview and open, promotion,
// and rollback all run it; services without a live claim are new and
// pass. Measuring usage happens only for an actual reduction.
func (s *Service) checkTransitions(ctx context.Context, environmentID uuid.UUID, definition compiler.ProjectDefinition) error {
	databases, err := s.st.ListLiveDatabaseClaimsByEnvironment(ctx, &environmentID)
	if err != nil {
		return fmt.Errorf("deploy: list live database claims: %w", err)
	}
	for _, live := range databases {
		requested, ok := definition.Databases[live.ServiceKey]
		if !ok {
			continue
		}
		if err := databaseTransition(live, requested); err != nil {
			return err
		}
	}
	buckets, err := s.st.ListLiveBucketClaimsByEnvironment(ctx, &environmentID)
	if err != nil {
		return fmt.Errorf("deploy: list live bucket claims: %w", err)
	}
	for _, live := range buckets {
		requested, ok := definition.Buckets[live.ServiceKey]
		if !ok {
			continue
		}
		if err := bucketTransition(live, requested); err != nil {
			return err
		}
		if quotaReduced(live.StorageQuotaBytes, requested.StorageQuotaBytes) {
			if err := s.checkQuotaFits(ctx, live, requested.StorageQuotaBytes); err != nil {
				return err
			}
		}
	}
	return nil
}

// databaseTransition refuses a change to a field that places a database.
func databaseTransition(live store.DatabaseClaim, requested compiler.DatabaseClaim) error {
	service := "databases." + live.ServiceKey
	if live.Engine != requested.Engine {
		return &TransitionError{Service: service, Field: "engine", From: live.Engine, To: requested.Engine}
	}
	// The compiler only admits catalog majors; an unparsable one is the
	// reconciler's to report, not a transition.
	if major, err := strconv.Atoi(strings.TrimSpace(requested.Version)); err == nil && int32(major) != live.Major {
		return &TransitionError{Service: service, Field: "version",
			From: strconv.Itoa(int(live.Major)), To: strconv.Itoa(major)}
	}
	if live.Isolation != requested.Isolation {
		return &TransitionError{Service: service, Field: "isolation", From: live.Isolation, To: requested.Isolation}
	}
	return nil
}

// bucketTransition refuses a change to a field that places a bucket.
func bucketTransition(live store.BucketClaim, requested compiler.BucketClaim) error {
	service := "buckets." + live.ServiceKey
	if live.Visibility != requested.Visibility {
		return &TransitionError{Service: service, Field: "visibility", From: live.Visibility, To: requested.Visibility}
	}
	if live.Versioning != requested.Versioning {
		return &TransitionError{Service: service, Field: "versioning", From: live.Versioning, To: requested.Versioning}
	}
	return nil
}

// quotaReduced reports whether a storage quota shrinks; zero is unlimited.
func quotaReduced(current, requested int64) bool {
	return requested > 0 && (current == 0 || requested < current)
}

// checkQuotaFits measures the bucket now and refuses a quota at or below
// its usage: at the quota, upkeep already refuses uploads.
func (s *Service) checkQuotaFits(ctx context.Context, live store.BucketClaim, quota int64) error {
	bucket := "buckets." + live.ServiceKey
	if s.bucketUsage == nil {
		return &BucketUsageUnavailableError{Bucket: bucket, QuotaBytes: quota, Err: errNoBucketUsage}
	}
	ctx, cancel := context.WithTimeout(ctx, bucketUsageTimeout)
	defer cancel()
	used, err := s.bucketUsage.BucketUsedBytes(ctx, live.ID)
	if err != nil {
		return &BucketUsageUnavailableError{Bucket: bucket, QuotaBytes: quota, Err: err}
	}
	if used >= quota {
		return &BucketQuotaError{Bucket: bucket, UsedBytes: used, QuotaBytes: quota}
	}
	return nil
}
