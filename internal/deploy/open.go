package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/artifact"
	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/buildstore"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/plan"
	"github.com/Hinkolas/skali/internal/redact"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/valuestore"
)

var (
	// ErrDestructiveChange: the plan removes or destroys service state and
	// the caller did not explicitly allow it.
	ErrDestructiveChange = errors.New("deploy: the plan contains destructive changes")
	// ErrRegistryDisabled: artifact work is required but no managed
	// registry is configured.
	ErrRegistryDisabled = errors.New("deploy: no managed registry configured")
	// Promotion source guards.
	ErrSourceEnvironmentNotFound = errors.New("deploy: source environment not found")
	ErrSameEnvironment           = errors.New("deploy: source and target environments are the same")
	ErrSourceProjectMismatch     = errors.New("deploy: source environment belongs to another project")
	ErrNoActiveRevision          = errors.New("deploy: source environment has no active revision")
)

// UnsupportedCapabilitiesError: the revision needs capabilities this
// installation does not declare.
type UnsupportedCapabilitiesError struct{ Missing []string }

func (e *UnsupportedCapabilitiesError) Error() string {
	return "deploy: this installation cannot run the definition; missing capabilities: " +
		strings.Join(e.Missing, ", ")
}

// UnsupportedBucketPolicyError: an authored bucket field whose policy has
// not landed yet (REWORK_V2 10.5: public access, versioning, and lifecycle
// rules remain later policies built on the same substrate). Rejected fast
// at deploy open, like a missing capability; the authoring schema keeps the
// field so definitions stay portable.
type UnsupportedBucketPolicyError struct {
	Bucket string
	Field  string
}

func (e *UnsupportedBucketPolicyError) Error() string {
	return "deploy: buckets." + e.Bucket + ": " + e.Field +
		" is not supported yet; it arrives with a later policy"
}

// validateBucketPolicies enforces the v1 bucket product surface: private
// visibility, storage quota, versioning disabled.
func validateBucketPolicies(definition compiler.ProjectDefinition) error {
	keys := make([]string, 0, len(definition.Buckets))
	for key := range definition.Buckets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		bucket := definition.Buckets[key]
		switch {
		case bucket.Visibility == "public-read":
			return &UnsupportedBucketPolicyError{Bucket: key, Field: "visibility: public-read"}
		case bucket.Versioning == "enabled":
			return &UnsupportedBucketPolicyError{Bucket: key, Field: "versioning: enabled"}
		case bucket.ObjectQuota > 0:
			return &UnsupportedBucketPolicyError{Bucket: key, Field: "quotas.objects"}
		case bucket.MaxObjectSizeBytes > 0:
			return &UnsupportedBucketPolicyError{Bucket: key, Field: "quotas.maxObjectSize"}
		case bucket.AbortIncompleteUploadsAfterSeconds > 0 || bucket.ExpireNoncurrentVersionsAfterSec > 0:
			return &UnsupportedBucketPolicyError{Bucket: key, Field: "lifecycle"}
		}
	}
	return nil
}

// MissingBuildInputError: a build-sourced application arrived without its
// client-computed input hashes.
type MissingBuildInputError struct{ Application string }

func (e *MissingBuildInputError) Error() string {
	return "deploy: application " + e.Application + " uses a build source but no build input hashes were submitted"
}

// PlatformMismatchError: a submitted build targets none of the observed
// cluster platforms, so the image could not run on any node.
type PlatformMismatchError struct {
	Application string
	Submitted   string
	Cluster     []string
}

func (e *PlatformMismatchError) Error() string {
	return "deploy: application " + e.Application + " would be built for " + e.Submitted +
		" but the cluster nodes run " + strings.Join(e.Cluster, ", ") +
		"; upgrade the skali CLI (newer versions build for the cluster platform automatically) or pass a matching --platform"
}

// ArtifactsIncompleteError: completion was requested before every artifact
// verified.
type ArtifactsIncompleteError struct{ Missing []string }

func (e *ArtifactsIncompleteError) Error() string {
	return "deploy: artifacts are not verified for: " + strings.Join(e.Missing, ", ")
}

// BuildInput carries the client-computed hashes of one build-sourced
// application; the input hash is the artifact dedup key.
type BuildInput struct {
	InputHash  string `json:"input_hash"`
	ConfigHash string `json:"config_hash"`
	Platform   string `json:"platform"`
}

// ArtifactAction is one per-application decision taken at open: reuse a
// verified artifact, build, or import. Stored on the deployment row so
// completion re-reads decisions instead of trusting the client.
type ArtifactAction struct {
	Application string    `json:"application"`
	Action      string    `json:"action"` // reuse | build | import
	Kind        string    `json:"kind"`   // revision artifact kind
	ArtifactID  uuid.UUID `json:"artifact_id,omitzero"`
	BuildID     uuid.UUID `json:"build_id,omitzero"`
	Upstream    string    `json:"upstream,omitempty"`
	InputHash   string    `json:"input_hash,omitempty"`
	Platform    string    `json:"platform,omitempty"`
	// Reference and Digest are set for reuse actions so clients can report
	// what is being reused.
	Reference string `json:"reference,omitempty"`
	Digest    string `json:"digest,omitempty"`
}

// PlanInput are the shared inputs of plan preview and open.
type PlanInput struct {
	EnvironmentID       uuid.UUID
	DefinitionVersionID uuid.UUID
	// FromEnvironmentID promotes the source environment's active revision:
	// its definition version and artifact set are reused verbatim while
	// values resolve for the target environment. Mutually exclusive with
	// DefinitionVersionID and BuildInputs.
	FromEnvironmentID uuid.UUID
	// CandidateID selects a staged values batch; uuid.Nil plans against
	// current values.
	CandidateID uuid.UUID
	BuildInputs map[string]BuildInput
	// NodePlatforms are the observed cluster platforms; empty skips the
	// platform guard (observation not synced, or an api-only server).
	NodePlatforms []string
	// Rebuild ignores artifact reuse entirely: every build-sourced
	// application builds again and every image source re-imports, so moved
	// upstream tags and refreshed base images are picked up.
	Rebuild bool
}

// Preview is a computed plan with its artifact decisions; nothing is
// created or mutated.
type Preview struct {
	Plan    *plan.Plan
	Actions []ArtifactAction
	// UpToDate: every artifact is reusable and the candidate revision
	// checksum equals the active revision. Deploying would be meaningless.
	UpToDate bool
	// Candidate is the revision the plan was computed against; its
	// checksum is only real when no placeholder artifacts were needed.
	Candidate *revision.Revision
	// Orphaned lists stored value names the definition no longer
	// references; they are ignored by deployments. Advisory only.
	Orphaned []string
}

type OpenInput struct {
	PlanInput
	Actor              string
	BuildExecutor      string
	AllowDestructive   bool
	Capabilities       []string
	RegistryConfigured bool
	Journal            *journal.Service
	// Force opens the deployment even when it is up to date; promotion then
	// stamps a workload restart so every application pod is recreated.
	// Stateful services are untouched: force recreates pods, never data.
	Force bool
}

// Opened is one accepted deployment: the coordination row, its run, and
// the work list the client executes. UpToDate short-circuits: nothing was
// created and there is nothing to do.
type Opened struct {
	Deployment *store.Deployment
	RunID      uuid.UUID
	Plan       *plan.Plan
	Actions    []ArtifactAction
	UpToDate   bool
	// Orphaned lists stored value names the definition no longer
	// references; they are ignored by deployments. Advisory only.
	Orphaned []string
}

// PlanPreview validates the candidate server-side and returns the semantic
// and destructive plan without mutating anything (transcript: planning
// never stores values, never moves targets).
func (s *Service) PlanPreview(ctx context.Context, in PlanInput) (*Preview, error) {
	if in.FromEnvironmentID != uuid.Nil {
		src, err := s.loadPromotionSource(ctx, in.EnvironmentID, in.FromEnvironmentID)
		if err != nil {
			return nil, err
		}
		in.DefinitionVersionID = src.DefinitionVersionID
		env, definitionVersion, definition, err := s.loadDefinition(ctx, in.EnvironmentID, in.DefinitionVersionID)
		if err != nil {
			return nil, err
		}
		return s.finishPreview(ctx, env, definitionVersion, definition, in.CandidateID, src.Actions, src.Artifacts, true)
	}
	env, definitionVersion, definition, err := s.loadDefinition(ctx, in.EnvironmentID, in.DefinitionVersionID)
	if err != nil {
		return nil, err
	}
	return s.preview(ctx, env, definitionVersion, definition, in)
}

// Open starts one deployment: it re-runs the plan gate, creates the run
// and the coordination row, journals the validation and value steps, and
// materializes pending artifact and build records for the client's work
// list. The environment's values, target, and active revision stay
// untouched until completion.
func (s *Service) Open(ctx context.Context, in OpenInput) (*Opened, error) {
	var src *promotionSource
	if in.FromEnvironmentID != uuid.Nil {
		var err error
		if src, err = s.loadPromotionSource(ctx, in.EnvironmentID, in.FromEnvironmentID); err != nil {
			return nil, err
		}
		in.DefinitionVersionID = src.DefinitionVersionID
	}
	env, definitionVersion, definition, err := s.loadDefinition(ctx, in.EnvironmentID, in.DefinitionVersionID)
	if err != nil {
		return nil, err
	}
	// The in-flight gate first: with a window already open, every other
	// verdict (plan, values, capabilities) would mislead. The partial
	// unique index still backs this against races.
	if _, err := s.st.GetPreparingDeploymentForEnvironment(ctx, in.EnvironmentID); err == nil {
		return nil, ErrDeploymentInFlight
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("deploy: check in-flight deployment: %w", err)
	}
	if missing := missingCapabilities(revision.RequiredCapabilities(definition), in.Capabilities); len(missing) > 0 {
		return nil, &UnsupportedCapabilitiesError{Missing: missing}
	}
	if err := validateBucketPolicies(definition); err != nil {
		return nil, err
	}
	var preview *Preview
	if src != nil {
		preview, err = s.finishPreview(ctx, env, definitionVersion, definition, in.CandidateID, src.Actions, src.Artifacts, true)
	} else {
		preview, err = s.preview(ctx, env, definitionVersion, definition, in.PlanInput)
	}
	if err != nil {
		return nil, err
	}
	if preview.Plan.Destructive() && !in.AllowDestructive {
		return nil, ErrDestructiveChange
	}
	if preview.UpToDate && !in.Force {
		return &Opened{Plan: preview.Plan, Actions: preview.Actions, UpToDate: true, Orphaned: preview.Orphaned}, nil
	}
	needsArtifactWork := false
	for _, action := range preview.Actions {
		if action.Action != "reuse" {
			needsArtifactWork = true
		}
	}
	if needsArtifactWork && !in.RegistryConfigured {
		return nil, ErrRegistryDisabled
	}

	run, err := in.Journal.CreateRun(ctx, journal.RunInput{
		Kind:          "deployment",
		ProjectID:     env.ProjectID,
		EnvironmentID: env.ID,
		Actor:         in.Actor,
	})
	if err != nil {
		return nil, err
	}
	if err := in.Journal.StartRun(ctx, run.ID); err != nil {
		if errors.Is(err, journal.ErrRunConflict) {
			return nil, ErrDeploymentInFlight
		}
		return nil, err
	}

	opened, err := s.openUnderRun(ctx, in, env, run.ID, preview)
	if err != nil {
		// The window never opened; the run must not stay running.
		if finishErr := in.Journal.FinishRun(ctx, run.ID, journal.RunFailed); finishErr != nil &&
			!errors.Is(finishErr, journal.ErrInvalidTransition) {
			return nil, fmt.Errorf("deploy: close run after failed open: %w (original: %w)", finishErr, err)
		}
		return nil, err
	}
	return opened, nil
}

func (s *Service) openUnderRun(ctx context.Context, in OpenInput, env store.Environment, runID uuid.UUID, preview *Preview) (*Opened, error) {
	redactor, err := s.values.Redactor(ctx, env.ID, in.CandidateID)
	if err != nil {
		return nil, err
	}

	// Materialize the client's work list: a pending artifact per build or
	// import, plus a running build record per build (the builds table, not
	// the journal, drives their staleness).
	actions := make([]ArtifactAction, len(preview.Actions))
	copy(actions, preview.Actions)
	for index := range actions {
		action := &actions[index]
		if action.Action == "reuse" {
			continue
		}
		pending, err := s.artifacts.CreatePending(ctx, artifactstore.Pending{
			ProjectID:   env.ProjectID,
			Application: action.Application,
			Kind:        action.Kind,
			Upstream:    action.Upstream,
			ContextHash: action.InputHash,
		})
		if err != nil {
			return nil, err
		}
		action.ArtifactID = pending.ID
	}

	encodedActions, err := json.Marshal(actions)
	if err != nil {
		return nil, fmt.Errorf("deploy: encode actions: %w", err)
	}
	deployment, err := s.CreateDeployment(ctx, NewDeployment{
		ProjectID:           env.ProjectID,
		EnvironmentID:       env.ID,
		DefinitionVersionID: in.DefinitionVersionID,
		CandidateID:         in.CandidateID,
		RunID:               runID,
		Actor:               in.Actor,
		BuildExecutor:       in.BuildExecutor,
		Actions:             encodedActions,
		Restart:             in.Force,
	})
	if err != nil {
		return nil, err
	}
	for index := range actions {
		action := &actions[index]
		if action.Action != "build" {
			continue
		}
		build, err := s.builds.CreateLocal(ctx, buildstore.Local{
			ProjectID:    env.ProjectID,
			DeploymentID: deployment.ID,
			Application:  action.Application,
			Platform:     action.Platform,
			ContextHash:  action.InputHash,
			ConfigHash:   in.BuildInputs[action.Application].ConfigHash,
			ArtifactID:   action.ArtifactID,
			RunID:        runID,
			StepKey:      "artifacts." + action.Application + ".build",
		})
		if err != nil {
			return nil, err
		}
		action.BuildID = build.ID
	}
	if len(actions) > 0 {
		encodedActions, err = json.Marshal(actions)
		if err != nil {
			return nil, fmt.Errorf("deploy: encode actions: %w", err)
		}
		if err := s.st.SetDeploymentActions(ctx, store.SetDeploymentActionsParams{
			ID: deployment.ID, Actions: encodedActions,
		}); err != nil {
			return nil, fmt.Errorf("deploy: store actions: %w", err)
		}
		deployment.Actions = encodedActions
	}

	// The server-owned opening steps of the transcript tree.
	if err := s.instantStep(ctx, in.Journal, runID, redactor, "validate", "Validate project definition",
		"definition "+shortHash(preview.Candidate.DefinitionHash)+" validated"); err != nil {
		return nil, err
	}
	valuesLine := "using current environment values"
	if in.CandidateID != uuid.Nil {
		staged := countCandidate(ctx, s.values, env.ID, in.CandidateID)
		valuesLine = fmt.Sprintf("%d values staged", staged)
	}
	if err := s.instantStep(ctx, in.Journal, runID, redactor, "values", "Prepare environment values", valuesLine); err != nil {
		return nil, err
	}
	if _, err := in.Journal.EnsureStep(ctx, runID, nil, "artifacts", "Prepare artifacts"); err != nil {
		return nil, err
	}

	return &Opened{
		Deployment: deployment,
		RunID:      runID,
		Plan:       preview.Plan,
		Actions:    actions,
		Orphaned:   preview.Orphaned,
	}, nil
}

// Complete closes the artifact window: every recorded action must reference
// a verified artifact, then revision creation, promotion, and the rollout
// handoff run under the deployment's own run. Failure leaves values,
// target, and active revision untouched.
func (s *Service) Complete(ctx context.Context, deploymentID uuid.UUID, jsvc *journal.Service) (*ExecuteResult, error) {
	deployment, err := s.GetDeployment(ctx, deploymentID)
	if err != nil {
		return nil, err
	}
	if deployment.Status != string(DeploymentPreparing) {
		return nil, fmt.Errorf("%w: %s deployments cannot complete", ErrInvalidDeploymentTransition, deployment.Status)
	}
	var actions []ArtifactAction
	if err := json.Unmarshal(deployment.Actions, &actions); err != nil {
		return nil, fmt.Errorf("deploy: decode actions: %w", err)
	}

	ids := make(map[string]uuid.UUID, len(actions))
	var missing []string
	for _, action := range actions {
		record, err := s.artifacts.Get(ctx, action.ArtifactID)
		if err != nil || artifact.Phase(record.Phase) != artifact.PhaseVerified {
			missing = append(missing, action.Application)
			continue
		}
		ids[action.Application] = record.ID
	}
	if len(missing) > 0 {
		return nil, &ArtifactsIncompleteError{Missing: missing}
	}

	runID := uuid.Nil
	if deployment.RunID != nil {
		runID = *deployment.RunID
	} else {
		// The journal never drives: if its rows were removed mid-window,
		// the remaining stages still run under a fresh run.
		run, err := jsvc.CreateRun(ctx, journal.RunInput{
			Kind: "deployment", ProjectID: deployment.ProjectID,
			EnvironmentID: deployment.EnvironmentID, Actor: deployment.Actor,
		})
		if err != nil {
			return nil, err
		}
		if err := jsvc.StartRun(ctx, run.ID); err != nil && !errors.Is(err, journal.ErrRunConflict) {
			return nil, err
		}
		runID = run.ID
	}

	if err := s.closeArtifactsStep(ctx, jsvc, runID, deployment, actions); err != nil {
		return nil, err
	}

	candidateID := uuid.Nil
	if deployment.CandidateID != nil {
		candidateID = *deployment.CandidateID
	}
	result, err := s.runStages(ctx, runID, ExecuteInput{
		ProjectID:           deployment.ProjectID,
		EnvironmentID:       deployment.EnvironmentID,
		DefinitionVersionID: deployment.DefinitionVersionID,
		CandidateID:         candidateID,
		Resolver:            &artifactstore.RecordResolver{Store: s.artifacts, IDs: ids},
		Journal:             jsvc,
		Actor:               deployment.Actor,
		Restart:             deployment.Restart,
	})
	if err != nil {
		if statusErr := s.setDeploymentStatus(ctx, deploymentID, DeploymentFailed, uuid.Nil); statusErr != nil {
			return result, fmt.Errorf("deploy: mark deployment failed: %w (original: %w)", statusErr, err)
		}
		return result, err
	}
	if err := s.setDeploymentStatus(ctx, deploymentID, DeploymentPromoted, result.RevisionID); err != nil {
		return result, err
	}
	return result, nil
}

// FailDeployment closes an open artifact window as failed: pending
// artifacts are abandoned, running builds fail, the staged candidate is
// discarded, and the run finishes failed. Values, target, and active
// revision are untouched by construction.
func (s *Service) FailDeployment(ctx context.Context, deploymentID uuid.UUID, jsvc *journal.Service) error {
	return s.closeDeployment(ctx, deploymentID, jsvc, DeploymentFailed)
}

// CancelDeployment is FailDeployment with cancellation semantics.
func (s *Service) CancelDeployment(ctx context.Context, deploymentID uuid.UUID, jsvc *journal.Service) error {
	return s.closeDeployment(ctx, deploymentID, jsvc, DeploymentCancelled)
}

func (s *Service) closeDeployment(ctx context.Context, deploymentID uuid.UUID, jsvc *journal.Service, to DeploymentStatus) error {
	deployment, err := s.GetDeployment(ctx, deploymentID)
	if err != nil {
		return err
	}
	// Claim the terminal state first; a concurrent Complete loses cleanly.
	if err := s.setDeploymentStatus(ctx, deploymentID, to, uuid.Nil); err != nil {
		return err
	}
	var actions []ArtifactAction
	if err := json.Unmarshal(deployment.Actions, &actions); err != nil {
		return fmt.Errorf("deploy: decode actions: %w", err)
	}
	for _, action := range actions {
		if action.Action == "reuse" {
			continue
		}
		if err := s.artifacts.Abandon(ctx, action.ArtifactID); err != nil &&
			!errors.Is(err, artifactstore.ErrInvalidTransition) && !errors.Is(err, artifactstore.ErrNotFound) {
			return err
		}
	}
	builds, err := s.builds.ListForDeployment(ctx, deploymentID)
	if err != nil {
		return err
	}
	buildOutcome := buildstore.StatusFailed
	runOutcome := journal.RunFailed
	if to == DeploymentCancelled {
		buildOutcome = buildstore.StatusCancelled
		runOutcome = journal.RunCancelled
	}
	for _, build := range builds {
		if build.Status != "running" {
			continue
		}
		if err := s.builds.Finish(ctx, build.ID, buildOutcome); err != nil {
			return err
		}
	}
	if deployment.CandidateID != nil {
		if err := s.values.DiscardCandidate(ctx, deployment.EnvironmentID, *deployment.CandidateID); err != nil {
			return err
		}
	}
	if deployment.RunID != nil {
		if err := jsvc.FinishRun(ctx, *deployment.RunID, runOutcome); err != nil &&
			!errors.Is(err, journal.ErrInvalidTransition) && !errors.Is(err, journal.ErrNotFound) {
			return err
		}
	}
	return nil
}

// SweepStaleDeployments fails preparing deployments whose client stopped
// reporting (no build heartbeat or artifact verification touched the row
// within the timeout).
func (s *Service) SweepStaleDeployments(ctx context.Context, jsvc *journal.Service, timeout time.Duration) (int, error) {
	rows, err := s.st.ListStalePreparingDeployments(ctx, time.Now().Add(-timeout))
	if err != nil {
		return 0, fmt.Errorf("deploy: list stale deployments: %w", err)
	}
	swept := 0
	for _, row := range rows {
		if err := s.FailDeployment(ctx, row.ID, jsvc); err != nil {
			if errors.Is(err, ErrInvalidDeploymentTransition) {
				continue
			}
			return swept, err
		}
		swept++
	}
	return swept, nil
}

// TouchDeployment records artifact-window liveness.
func (s *Service) TouchDeployment(ctx context.Context, deploymentID uuid.UUID) error {
	if _, err := s.st.TouchDeployment(ctx, deploymentID); err != nil {
		return fmt.Errorf("deploy: touch deployment: %w", err)
	}
	return nil
}

// platformsOverlap reports whether a submitted platform string (possibly a
// comma-joined list) targets at least one observed cluster platform, i.e.
// whether the resulting image could run anywhere. Either side being empty
// means unknown and skips the guard: partial coverage is a deliberate
// client choice, only a fully unrunnable build is rejected.
func platformsOverlap(submitted string, cluster []string) bool {
	if submitted == "" || len(cluster) == 0 {
		return true
	}
	targets := make(map[string]struct{})
	for platform := range strings.SplitSeq(submitted, ",") {
		if platform = strings.TrimSpace(platform); platform != "" {
			targets[platform] = struct{}{}
		}
	}
	if len(targets) == 0 {
		return true
	}
	for _, platform := range cluster {
		if _, ok := targets[platform]; ok {
			return true
		}
	}
	return false
}

// preview computes the plan and per-application artifact decisions without
// creating anything.
func (s *Service) preview(ctx context.Context, env store.Environment, definitionVersion store.DefinitionVersion,
	definition compiler.ProjectDefinition, in PlanInput) (*Preview, error) {

	actions := make([]ArtifactAction, 0, len(definition.Applications))
	artifacts := make(map[string]revision.Artifact, len(definition.Applications))
	allReuse := true
	for _, key := range sortedKeys(definition.Applications) {
		source := definition.Applications[key].Source
		if source.Kind == "image" {
			upstream := source.Image
			row, err := s.st.GetVerifiedArtifactByUpstream(ctx, upstream)
			if in.Rebuild && err == nil {
				// Rebuild discards the reusable row: the upstream is
				// resolved and imported again, picking up a moved tag.
				err = pgx.ErrNoRows
			}
			switch {
			case err == nil:
				actions = append(actions, ArtifactAction{
					Application: key, Action: "reuse", Kind: row.Kind, ArtifactID: row.ID,
					Upstream: row.Upstream, Reference: row.Reference, Digest: *row.Digest,
				})
				artifacts[key] = revision.Artifact{
					Reference: row.Reference, Digest: *row.Digest, Kind: row.Kind, Upstream: row.Upstream,
				}
			case errors.Is(err, pgx.ErrNoRows):
				allReuse = false
				actions = append(actions, ArtifactAction{
					Application: key, Action: "import", Kind: revision.KindImport, Upstream: upstream,
				})
				artifacts[key] = revision.Artifact{
					Reference: "pending", Digest: revision.PendingDigest,
					Kind: revision.KindImport, Upstream: upstream,
				}
			default:
				return nil, fmt.Errorf("deploy: look up import artifact: %w", err)
			}
			continue
		}

		input, ok := in.BuildInputs[key]
		if !ok || input.InputHash == "" {
			return nil, &MissingBuildInputError{Application: key}
		}
		if !platformsOverlap(input.Platform, in.NodePlatforms) {
			return nil, &PlatformMismatchError{Application: key, Submitted: input.Platform, Cluster: in.NodePlatforms}
		}
		row, err := s.st.GetReusableArtifact(ctx, store.GetReusableArtifactParams{
			ProjectID:   &env.ProjectID,
			Application: key,
			Kind:        revision.KindBuildLocal,
			ContextHash: input.InputHash,
		})
		if in.Rebuild && err == nil {
			// Rebuild discards the reusable row: the application builds
			// again even though a verified artifact matches the inputs.
			err = pgx.ErrNoRows
		}
		switch {
		case err == nil:
			actions = append(actions, ArtifactAction{
				Application: key, Action: "reuse", Kind: row.Kind, ArtifactID: row.ID,
				InputHash: input.InputHash, Platform: input.Platform,
				Reference: row.Reference, Digest: *row.Digest,
			})
			artifacts[key] = revision.Artifact{
				Reference: row.Reference, Digest: *row.Digest, Kind: row.Kind, ContextHash: row.ContextHash,
			}
		case errors.Is(err, pgx.ErrNoRows):
			allReuse = false
			actions = append(actions, ArtifactAction{
				Application: key, Action: "build", Kind: revision.KindBuildLocal,
				InputHash: input.InputHash, Platform: input.Platform,
			})
			artifacts[key] = revision.Artifact{
				Reference: "pending", Digest: revision.PendingDigest,
				Kind: revision.KindBuildLocal, ContextHash: input.InputHash,
			}
		default:
			return nil, fmt.Errorf("deploy: look up build artifact: %w", err)
		}
	}

	return s.finishPreview(ctx, env, definitionVersion, definition, in.CandidateID, actions, artifacts, allReuse)
}

// finishPreview resolves the target environment's values, builds the
// candidate revision over the decided artifact set, and diffs it against
// the active revision. Shared by the ordinary preview and the promotion
// path, which substitutes a source revision's artifact set.
func (s *Service) finishPreview(ctx context.Context, env store.Environment,
	definitionVersion store.DefinitionVersion, definition compiler.ProjectDefinition,
	candidateID uuid.UUID, actions []ArtifactAction,
	artifacts map[string]revision.Artifact, allReuse bool) (*Preview, error) {

	secretVersions, orphaned, err := s.resolveValues(ctx, env.ID, candidateID, definition.RequiredVariables)
	if err != nil {
		return nil, err
	}
	candidate, err := revision.Build(revision.Input{
		Result:          &compiler.Result{Hash: definitionVersion.DefinitionHash, Definition: definition},
		Environment:     env.Name,
		SecretVersions:  secretVersions,
		Artifacts:       artifacts,
		CompilerVersion: s.version,
	})
	if err != nil {
		return nil, fmt.Errorf("deploy: build candidate revision: %w", err)
	}

	var active *revision.Revision
	activeChecksum := ""
	target, err := s.st.GetEnvironmentTarget(ctx, env.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("deploy: get target: %w", err)
	}
	if err == nil && target.ActiveRevisionID != nil {
		active, err = s.GetRevision(ctx, *target.ActiveRevisionID)
		if err != nil {
			return nil, err
		}
		activeChecksum = active.Checksum
	}

	return &Preview{
		Plan:      plan.Diff(active, candidate),
		Actions:   actions,
		UpToDate:  allReuse && active != nil && candidate.Checksum == activeChecksum,
		Candidate: candidate,
		Orphaned:  orphaned,
	}, nil
}

// promotionSource is a source environment's active revision prepared for
// re-deployment into another environment of the same project: the stored
// definition version, the verbatim artifact set, and all-reuse actions over
// the revision's leased artifact rows.
type promotionSource struct {
	DefinitionVersionID uuid.UUID
	Actions             []ArtifactAction
	Artifacts           map[string]revision.Artifact
}

// loadPromotionSource resolves the source environment and its active
// revision. Artifact rows are recovered through the revision's leases and
// matched by reference and digest; reuse queries by context hash are never
// consulted, so the promotion pins exactly what the source runs.
func (s *Service) loadPromotionSource(ctx context.Context, environmentID, fromEnvironmentID uuid.UUID) (*promotionSource, error) {
	if fromEnvironmentID == environmentID {
		return nil, ErrSameEnvironment
	}
	env, err := s.st.GetEnvironmentByID(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrEnvironmentNotFound
		}
		return nil, fmt.Errorf("deploy: get environment: %w", err)
	}
	source, err := s.st.GetEnvironmentByID(ctx, fromEnvironmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSourceEnvironmentNotFound
		}
		return nil, fmt.Errorf("deploy: get source environment: %w", err)
	}
	if source.ProjectID != env.ProjectID {
		return nil, ErrSourceProjectMismatch
	}
	target, err := s.st.GetEnvironmentTarget(ctx, source.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoActiveRevision
		}
		return nil, fmt.Errorf("deploy: get source target: %w", err)
	}
	if target.ActiveRevisionID == nil {
		return nil, ErrNoActiveRevision
	}
	row, err := s.st.GetRevisionByID(ctx, *target.ActiveRevisionID)
	if err != nil {
		return nil, fmt.Errorf("deploy: get source revision: %w", err)
	}
	document, err := revision.Decode(row.Document)
	if err != nil {
		return nil, err
	}

	leases, err := s.st.ListArtifactLeasesByRevision(ctx, row.ID)
	if err != nil {
		return nil, fmt.Errorf("deploy: list source artifact leases: %w", err)
	}
	byIdentity := make(map[string]store.Artifact, len(leases))
	for _, lease := range leases {
		art, err := s.st.GetArtifactByID(ctx, lease.ArtifactID)
		if err != nil {
			return nil, fmt.Errorf("deploy: get leased artifact: %w", err)
		}
		if art.Digest != nil {
			byIdentity[art.Reference+"@"+*art.Digest] = art
		}
	}
	actions := make([]ArtifactAction, 0, len(document.Artifacts))
	for _, key := range sortedKeys(document.Artifacts) {
		art := document.Artifacts[key]
		artRow, ok := byIdentity[art.Reference+"@"+art.Digest]
		if !ok {
			return nil, fmt.Errorf("deploy: no leased artifact matches application %s of the source revision", key)
		}
		actions = append(actions, ArtifactAction{
			Application: key, Action: "reuse", Kind: art.Kind, ArtifactID: artRow.ID,
			Upstream: art.Upstream, InputHash: art.ContextHash,
			Reference: art.Reference, Digest: art.Digest,
		})
	}
	return &promotionSource{
		DefinitionVersionID: row.DefinitionVersionID,
		Actions:             actions,
		Artifacts:           document.Artifacts,
	}, nil
}

// loadDefinition loads and cross-checks the environment and definition
// version, and decodes the stored definition.
func (s *Service) loadDefinition(ctx context.Context, environmentID, definitionVersionID uuid.UUID) (store.Environment, store.DefinitionVersion, compiler.ProjectDefinition, error) {
	var definition compiler.ProjectDefinition
	env, err := s.st.GetEnvironmentByID(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return env, store.DefinitionVersion{}, definition, ErrEnvironmentNotFound
		}
		return env, store.DefinitionVersion{}, definition, fmt.Errorf("deploy: get environment: %w", err)
	}
	definitionVersion, err := s.st.GetDefinitionVersionByID(ctx, definitionVersionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return env, definitionVersion, definition, fmt.Errorf("deploy: definition version not found")
		}
		return env, definitionVersion, definition, fmt.Errorf("deploy: get definition version: %w", err)
	}
	if definitionVersion.ProjectID != env.ProjectID {
		return env, definitionVersion, definition, ErrDefinitionMismatch
	}
	definition, err = compiler.DecodeDefinition(definitionVersion.Definition)
	if err != nil {
		return env, definitionVersion, definition, err
	}
	return env, definitionVersion, definition, nil
}

// instantStep journals one server-owned step that begins and succeeds
// within the open request.
func (s *Service) instantStep(ctx context.Context, jsvc *journal.Service, runID uuid.UUID,
	redactor *redact.Redactor, key, title, message string) error {
	step, err := jsvc.EnsureStep(ctx, runID, nil, key, title)
	if err != nil {
		return err
	}
	if err := jsvc.SetStepStatus(ctx, step.ID, journal.StepRunning); err != nil {
		return err
	}
	attempt, err := jsvc.StartAttempt(ctx, step.ID)
	if err != nil {
		return err
	}
	_ = jsvc.Writer(attempt.ID, redactor).Info(ctx, message)
	if err := jsvc.FinishAttempt(ctx, attempt.ID, journal.AttemptSucceeded); err != nil {
		return err
	}
	return jsvc.SetStepStatus(ctx, step.ID, journal.StepSucceeded)
}

// closeArtifactsStep concludes the artifacts parent step once every
// artifact verified.
func (s *Service) closeArtifactsStep(ctx context.Context, jsvc *journal.Service, runID uuid.UUID,
	deployment *store.Deployment, actions []ArtifactAction) error {
	redactor, err := s.values.Redactor(ctx, deployment.EnvironmentID, uuid.Nil)
	if err != nil {
		return err
	}
	step, err := jsvc.EnsureStep(ctx, runID, nil, "artifacts", "Prepare artifacts")
	if err != nil {
		return err
	}
	if step.Status == string(journal.StepPending) || step.Status == string(journal.StepWaiting) {
		if err := jsvc.SetStepStatus(ctx, step.ID, journal.StepRunning); err != nil {
			return err
		}
	}
	attempt, err := jsvc.StartAttempt(ctx, step.ID)
	if err != nil && !errors.Is(err, journal.ErrAttemptConflict) {
		return err
	}
	if attempt != nil {
		writer := jsvc.Writer(attempt.ID, redactor)
		for _, action := range actions {
			_ = writer.Info(ctx, action.Application+": "+action.Action+" verified")
		}
		if err := jsvc.FinishAttempt(ctx, attempt.ID, journal.AttemptSucceeded); err != nil {
			return err
		}
	}
	return jsvc.SetStepStatus(ctx, step.ID, journal.StepSucceeded)
}

func countCandidate(ctx context.Context, values *valuestore.Service, environmentID, candidateID uuid.UUID) int {
	staged, err := values.StagedVersions(ctx, environmentID, candidateID)
	if err != nil {
		return 0
	}
	return len(staged)
}

func missingCapabilities(required, available []string) []string {
	have := make(map[string]bool, len(available))
	for _, capability := range available {
		have[strings.TrimSpace(capability)] = true
	}
	var missing []string
	for _, capability := range required {
		if !have[capability] {
			missing = append(missing, capability)
		}
	}
	return missing
}

func shortHash(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}
