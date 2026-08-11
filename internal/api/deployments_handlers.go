package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/buildstore"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/plan"
	"github.com/Hinkolas/skali/internal/reconcile"
	"github.com/Hinkolas/skali/internal/registry"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
)

// deploymentsHandlers is the deployment coordination surface: plan preview,
// the open/complete artifact window, server-side digest verification, build
// heartbeats, and run cancellation with the automatic-fallback policy.
type deploymentsHandlers struct {
	st           *store.Store
	deploy       *deploy.Service
	artifacts    *artifactstore.Service
	builds       *buildstore.Service
	journal      *journal.Service
	registry     *registry.Client
	reconcile    *reconcile.Kernel
	capabilities []string
	managed      bool
}

type localApplicationPayload struct {
	Ports map[string]int32 `json:"ports"`
}

func decodeLocalApplications(payload map[string]localApplicationPayload) map[string]deploy.LocalApplication {
	if len(payload) == 0 {
		return nil
	}
	locals := make(map[string]deploy.LocalApplication, len(payload))
	for application, local := range payload {
		locals[application] = deploy.LocalApplication{Ports: local.Ports}
	}
	return locals
}

type buildInputPayload struct {
	InputHash  string `json:"input_hash"`
	ConfigHash string `json:"config_hash"`
	Platform   string `json:"platform"`
}

type artifactActionPayload struct {
	Application string `json:"application"`
	Action      string `json:"action"`
	Kind        string `json:"kind"`
	ArtifactID  string `json:"artifact_id,omitempty"`
	BuildID     string `json:"build_id,omitempty"`
	Upstream    string `json:"upstream,omitempty"`
	Platform    string `json:"platform,omitempty"`
	Reference   string `json:"reference,omitempty"`
	Digest      string `json:"digest,omitempty"`
	// PushRef is where a build or import client pushes; assembled from the
	// managed registry host and the contract repository layout.
	PushRef string `json:"push_ref,omitempty"`
	// StepKey roots the client-owned journal steps of this application.
	StepKey string `json:"step_key,omitempty"`
}

type deploymentPayload struct {
	ID                  string `json:"id"`
	ProjectID           string `json:"project_id"`
	EnvironmentID       string `json:"environment_id"`
	DefinitionVersionID string `json:"definition_version_id"`
	Status              string `json:"status"`
	RevisionID          string `json:"revision_id,omitempty"`
	RunID               string `json:"run_id,omitempty"`
	BuildExecutor       string `json:"build_executor"`
}

func (h *deploymentsHandlers) actionPayloads(ctx context.Context, projectID uuid.UUID, actions []deploy.ArtifactAction) []artifactActionPayload {
	projectName := ""
	if project, err := h.st.GetProjectByID(ctx, projectID); err == nil {
		projectName = project.Name
	}
	payloads := make([]artifactActionPayload, len(actions))
	for index, action := range actions {
		payload := artifactActionPayload{
			Application: action.Application,
			Action:      action.Action,
			Kind:        action.Kind,
			Upstream:    action.Upstream,
			Platform:    action.Platform,
			Reference:   action.Reference,
			Digest:      action.Digest,
			StepKey:     "artifacts." + action.Application,
		}
		if action.ArtifactID != uuid.Nil {
			payload.ArtifactID = action.ArtifactID.String()
		}
		if action.BuildID != uuid.Nil {
			payload.BuildID = action.BuildID.String()
		}
		if !h.registry.Disabled() {
			switch action.Action {
			case "build":
				payload.PushRef = h.registry.PushRef(registry.ReleaseRepo(projectName, action.Application), shortDigestTag(action.InputHash))
			case "import":
				if repo, err := registry.CacheRepo(action.Upstream); err == nil {
					payload.PushRef = h.registry.PushRef(repo, "imported")
				}
			}
		}
		payloads[index] = payload
	}
	return payloads
}

func decodeBuildInputs(payload map[string]buildInputPayload) map[string]deploy.BuildInput {
	inputs := make(map[string]deploy.BuildInput, len(payload))
	for application, input := range payload {
		inputs[application] = deploy.BuildInput{
			InputHash:  input.InputHash,
			ConfigHash: input.ConfigHash,
			Platform:   input.Platform,
		}
	}
	return inputs
}

// POST /v1/environments/{id}/plan
func (h *deploymentsHandlers) plan(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		DefinitionVersionID string                             `json:"definition_version_id"`
		FromEnvironmentID   string                             `json:"from_environment_id"`
		CandidateID         string                             `json:"candidate_id"`
		Builds              map[string]buildInputPayload       `json:"builds"`
		Rebuild             bool                               `json:"rebuild"`
		LocalApplications   map[string]localApplicationPayload `json:"local_applications"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	definitionVersionID, fromEnvironmentID, candidateID, ok := parseDeploymentSelector(w,
		req.DefinitionVersionID, req.FromEnvironmentID, req.CandidateID, len(req.Builds), req.Rebuild)
	if !ok {
		return
	}
	env, err := h.st.GetEnvironmentByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, codeNotFound, "not found")
			return
		}
		writeInternalError(r.Context(), w, "get environment", err)
		return
	}
	preview, err := h.deploy.PlanPreview(r.Context(), deploy.PlanInput{
		EnvironmentID:       id,
		DefinitionVersionID: definitionVersionID,
		FromEnvironmentID:   fromEnvironmentID,
		CandidateID:         candidateID,
		BuildInputs:         decodeBuildInputs(req.Builds),
		NodePlatforms:       h.reconcile.NodePlatforms(),
		Rebuild:             req.Rebuild,
		LocalApplications:   decodeLocalApplications(req.LocalApplications),
		ManagedCluster:      h.managed,
	})
	if err != nil {
		writeDeployError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Plan     *plan.Plan              `json:"plan"`
		Actions  []artifactActionPayload `json:"actions"`
		UpToDate bool                    `json:"up_to_date"`
		Orphaned []string                `json:"orphaned,omitempty"`
	}{preview.Plan, h.actionPayloads(r.Context(), env.ProjectID, preview.Actions), preview.UpToDate, preview.Orphaned})
}

// POST /v1/environments/{id}/deployments
func (h *deploymentsHandlers) open(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		DefinitionVersionID string                             `json:"definition_version_id"`
		FromEnvironmentID   string                             `json:"from_environment_id"`
		CandidateID         string                             `json:"candidate_id"`
		BuildExecutor       string                             `json:"build_executor"`
		AllowDestructive    bool                               `json:"allow_destructive"`
		Builds              map[string]buildInputPayload       `json:"builds"`
		Force               bool                               `json:"force"`
		Rebuild             bool                               `json:"rebuild"`
		LocalApplications   map[string]localApplicationPayload `json:"local_applications"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	definitionVersionID, fromEnvironmentID, candidateID, ok := parseDeploymentSelector(w,
		req.DefinitionVersionID, req.FromEnvironmentID, req.CandidateID, len(req.Builds), req.Rebuild)
	if !ok {
		return
	}
	if req.BuildExecutor != "" && req.BuildExecutor != "local" {
		writeError(w, http.StatusBadRequest, codeBadRequest,
			"build_executor must be local (cloud builders arrive with R4)")
		return
	}
	user := UserFrom(r.Context())
	opened, err := h.deploy.Open(r.Context(), deploy.OpenInput{
		PlanInput: deploy.PlanInput{
			EnvironmentID:       id,
			DefinitionVersionID: definitionVersionID,
			FromEnvironmentID:   fromEnvironmentID,
			CandidateID:         candidateID,
			BuildInputs:         decodeBuildInputs(req.Builds),
			NodePlatforms:       h.reconcile.NodePlatforms(),
			Rebuild:             req.Rebuild,
			LocalApplications:   decodeLocalApplications(req.LocalApplications),
			ManagedCluster:      h.managed,
		},
		Actor:              user.ID.String(),
		BuildExecutor:      req.BuildExecutor,
		AllowDestructive:   req.AllowDestructive,
		Capabilities:       h.capabilities,
		RegistryConfigured: !h.registry.Disabled(),
		Journal:            h.journal,
		Force:              req.Force,
	})
	if err != nil {
		writeDeployError(r.Context(), w, err)
		return
	}
	if opened.UpToDate {
		writeJSON(w, http.StatusOK, struct {
			UpToDate bool       `json:"up_to_date"`
			Plan     *plan.Plan `json:"plan"`
		}{true, opened.Plan})
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		Deployment deploymentPayload       `json:"deployment"`
		Plan       *plan.Plan              `json:"plan"`
		Actions    []artifactActionPayload `json:"actions"`
		UpToDate   bool                    `json:"up_to_date"`
		Orphaned   []string                `json:"orphaned,omitempty"`
	}{
		Deployment: newDeploymentPayload(opened.Deployment),
		Plan:       opened.Plan,
		Actions:    h.actionPayloads(r.Context(), opened.Deployment.ProjectID, opened.Actions),
		Orphaned:   opened.Orphaned,
	})
}

// GET /v1/deployments/{id}
func (h *deploymentsHandlers) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	deployment, err := h.deploy.GetDeployment(r.Context(), id)
	if err != nil {
		writeDeployError(r.Context(), w, err)
		return
	}
	var actions []deploy.ArtifactAction
	_ = json.Unmarshal(deployment.Actions, &actions)
	writeJSON(w, http.StatusOK, struct {
		Deployment deploymentPayload       `json:"deployment"`
		Actions    []artifactActionPayload `json:"actions"`
	}{newDeploymentPayload(deployment), h.actionPayloads(r.Context(), deployment.ProjectID, actions)})
}

// POST /v1/deployments/{id}/complete
func (h *deploymentsHandlers) complete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	result, err := h.deploy.Complete(r.Context(), id, h.journal)
	if err != nil {
		writeDeployError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		RunID      string `json:"run_id"`
		RevisionID string `json:"revision_id"`
	}{result.RunID.String(), result.RevisionID.String()})
}

// POST /v1/deployments/{id}/fail
func (h *deploymentsHandlers) fail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.deploy.FailDeployment(r.Context(), id, h.journal); err != nil {
		writeDeployError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "failed"})
}

// POST /v1/artifacts/{id}/verify
func (h *deploymentsHandlers) verifyArtifact(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		DeploymentID string `json:"deployment_id"`
		Digest       string `json:"digest"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	deploymentID, err := uuid.Parse(req.DeploymentID)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "deployment_id must be a UUID")
		return
	}
	if req.Digest == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "digest is required")
		return
	}
	deployment, err := h.deploy.GetDeployment(r.Context(), deploymentID)
	if err != nil {
		writeDeployError(r.Context(), w, err)
		return
	}
	var actions []deploy.ArtifactAction
	if err := json.Unmarshal(deployment.Actions, &actions); err != nil {
		writeInternalError(r.Context(), w, "decode deployment actions", err)
		return
	}
	var action *deploy.ArtifactAction
	for index := range actions {
		if actions[index].ArtifactID == id {
			action = &actions[index]
		}
	}
	if action == nil {
		writeError(w, http.StatusNotFound, codeNotFound, "the deployment does not expect this artifact")
		return
	}

	repository := ""
	if action.Kind == revision.KindImport {
		repository, err = registry.CacheRepo(action.Upstream)
		if err != nil {
			writeInternalError(r.Context(), w, "derive cache repository", err)
			return
		}
	} else {
		project, err := h.st.GetProjectByID(r.Context(), deployment.ProjectID)
		if err != nil {
			writeInternalError(r.Context(), w, "get project", err)
			return
		}
		repository = registry.ReleaseRepo(project.Name, action.Application)
	}

	// The registry itself is the authority; client-reported success is
	// never trusted as deployment state.
	if err := h.registry.VerifyManifest(r.Context(), repository, req.Digest); err != nil {
		writeDeployError(r.Context(), w, err)
		return
	}
	provenance, err := json.Marshal(map[string]string{
		"executor":      deployment.BuildExecutor,
		"deployment_id": deployment.ID.String(),
	})
	if err != nil {
		writeInternalError(r.Context(), w, "encode provenance", err)
		return
	}
	reference := h.registry.Host + "/" + repository
	if err := h.artifacts.Verify(r.Context(), id, reference, req.Digest, provenance); err != nil {
		if errors.Is(err, artifactstore.ErrInvalidTransition) {
			writeError(w, http.StatusConflict, codeConflict, "the artifact is not pending")
			return
		}
		if errors.Is(err, artifactstore.ErrNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "not found")
			return
		}
		writeInternalError(r.Context(), w, "verify artifact", err)
		return
	}
	// Close the producing build and record window liveness.
	if build, err := h.st.GetBuildByArtifactID(r.Context(), &id); err == nil {
		if err := h.builds.Finish(r.Context(), build.ID, buildstore.StatusSucceeded); err != nil &&
			!errors.Is(err, buildstore.ErrInvalidTransition) {
			writeInternalError(r.Context(), w, "finish build", err)
			return
		}
	}
	_ = h.deploy.TouchDeployment(r.Context(), deployment.ID)

	record, err := h.artifacts.Get(r.Context(), id)
	if err != nil {
		writeInternalError(r.Context(), w, "read artifact", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		ID        string `json:"id"`
		Phase     string `json:"phase"`
		Reference string `json:"reference"`
		Digest    string `json:"digest"`
	}{record.ID.String(), record.Phase, record.Reference, *record.Digest})
}

// POST /v1/builds/{id}/heartbeat
func (h *deploymentsHandlers) heartbeatBuild(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	alive, err := h.builds.Heartbeat(r.Context(), id)
	if err != nil {
		writeInternalError(r.Context(), w, "heartbeat build", err)
		return
	}
	if alive {
		if build, err := h.builds.Get(r.Context(), id); err == nil && build.DeploymentID != nil {
			_ = h.deploy.TouchDeployment(r.Context(), *build.DeploymentID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"alive": alive})
}

// POST /v1/runs/{id}/cancel: cancellation is explicit (detaching never
// cancels) and applies the product policy: before promotion the artifact
// window closes; after promotion but before activation the target returns
// to the prior active revision; after activation cancellation is refused.
func (h *deploymentsHandlers) cancelRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	run, err := h.journal.Run(r.Context(), id)
	if err != nil {
		if errors.Is(err, journal.ErrNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "not found")
			return
		}
		writeInternalError(r.Context(), w, "get run", err)
		return
	}
	if run.Status != string(journal.RunPending) && run.Status != string(journal.RunRunning) {
		writeError(w, http.StatusConflict, codeConflict, "the run is already "+run.Status)
		return
	}

	fallback := false
	deployment, err := h.st.GetDeploymentByRunID(r.Context(), &id)
	switch {
	case err == nil && deployment.Status == string(deploy.DeploymentPreparing):
		if err := h.deploy.CancelDeployment(r.Context(), deployment.ID, h.journal); err != nil {
			writeDeployError(r.Context(), w, err)
			return
		}
	case err == nil && deployment.Status == string(deploy.DeploymentPromoted):
		if err := h.journal.FinishRun(r.Context(), id, journal.RunCancelled); err != nil &&
			!errors.Is(err, journal.ErrInvalidTransition) {
			writeInternalError(r.Context(), w, "cancel run", err)
			return
		}
		if deployment.RevisionID != nil {
			rows, err := h.st.FallbackEnvironmentTarget(r.Context(), store.FallbackEnvironmentTargetParams{
				EnvironmentID:    deployment.EnvironmentID,
				TargetRevisionID: deployment.RevisionID,
			})
			if err != nil {
				writeInternalError(r.Context(), w, "fall back target", err)
				return
			}
			if rows > 0 {
				fallback = true
				h.reconcile.Enqueue(deployment.EnvironmentID)
			}
		}
	case err == nil || errors.Is(err, pgx.ErrNoRows):
		// Runs without a deployment row (reconcile runs, rollback runs,
		// pre-R3 rows): the journal is cancelled, and a rollback additionally
		// returns the target to the prior active revision like a cancelled
		// promotion.
		if err := h.journal.FinishRun(r.Context(), id, journal.RunCancelled); err != nil &&
			!errors.Is(err, journal.ErrInvalidTransition) {
			writeInternalError(r.Context(), w, "cancel run", err)
			return
		}
		if run.Kind == "rollback" && run.EnvironmentID != nil {
			target, err := h.st.GetEnvironmentTarget(r.Context(), *run.EnvironmentID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				writeInternalError(r.Context(), w, "get environment target", err)
				return
			}
			if err == nil && target.TargetRevisionID != nil {
				rows, err := h.st.FallbackEnvironmentTarget(r.Context(), store.FallbackEnvironmentTargetParams{
					EnvironmentID:    *run.EnvironmentID,
					TargetRevisionID: target.TargetRevisionID,
				})
				if err != nil {
					writeInternalError(r.Context(), w, "fall back target", err)
					return
				}
				if rows > 0 {
					fallback = true
					h.reconcile.Enqueue(*run.EnvironmentID)
				}
			}
		}
	default:
		writeInternalError(r.Context(), w, "get deployment for run", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Status   string `json:"status"`
		Fallback bool   `json:"fallback"`
	}{"cancelled", fallback})
}

func newDeploymentPayload(deployment *store.Deployment) deploymentPayload {
	payload := deploymentPayload{
		ID:                  deployment.ID.String(),
		ProjectID:           deployment.ProjectID.String(),
		EnvironmentID:       deployment.EnvironmentID.String(),
		DefinitionVersionID: deployment.DefinitionVersionID.String(),
		Status:              deployment.Status,
		BuildExecutor:       deployment.BuildExecutor,
	}
	if deployment.RevisionID != nil {
		payload.RevisionID = deployment.RevisionID.String()
	}
	if deployment.RunID != nil {
		payload.RunID = deployment.RunID.String()
	}
	return payload
}

func parseDeploymentIDs(w http.ResponseWriter, definitionVersion, candidate string) (uuid.UUID, uuid.UUID, bool) {
	definitionVersionID, err := uuid.Parse(definitionVersion)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "definition_version_id must be a UUID")
		return uuid.Nil, uuid.Nil, false
	}
	candidateID, ok := parseCandidateID(w, candidate)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	return definitionVersionID, candidateID, true
}

func parseCandidateID(w http.ResponseWriter, candidate string) (uuid.UUID, bool) {
	if candidate == "" {
		return uuid.Nil, true
	}
	candidateID, err := uuid.Parse(candidate)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "candidate_id must be a UUID")
		return uuid.Nil, false
	}
	return candidateID, true
}

// parseDeploymentSelector parses the revision selector shared by plan and
// open: exactly one of definition_version_id (an ordinary deployment) or
// from_environment_id (a promotion), plus the optional candidate.
func parseDeploymentSelector(w http.ResponseWriter, definitionVersion, from, candidate string,
	buildCount int, rebuild bool) (definitionVersionID, fromEnvironmentID, candidateID uuid.UUID, ok bool) {
	if from == "" {
		definitionVersionID, candidateID, ok = parseDeploymentIDs(w, definitionVersion, candidate)
		return definitionVersionID, uuid.Nil, candidateID, ok
	}
	if definitionVersion != "" || buildCount > 0 || rebuild {
		writeError(w, http.StatusBadRequest, codeBadRequest,
			"from_environment_id cannot be combined with definition_version_id, builds, or rebuild")
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}
	fromEnvironmentID, err := uuid.Parse(from)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "from_environment_id must be a UUID")
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}
	candidateID, ok = parseCandidateID(w, candidate)
	if !ok {
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}
	return uuid.Nil, fromEnvironmentID, candidateID, true
}

// writeDeployError maps deploy and registry sentinel errors onto the
// envelope.
func writeDeployError(ctx context.Context, w http.ResponseWriter, err error) {
	var capabilities *deploy.UnsupportedCapabilitiesError
	var bucketPolicy *deploy.UnsupportedBucketPolicyError
	var missingInput *deploy.MissingBuildInputError
	var incomplete *deploy.ArtifactsIncompleteError
	var platformMismatch *deploy.PlatformMismatchError
	var localsUnsupported *deploy.LocalApplicationsUnsupportedError
	var unknownLocal *deploy.UnknownLocalApplicationError
	var invalidIntercept *deploy.InvalidInterceptPortsError
	var invalidValues *revision.ValuesError
	var staleRevision *revision.SchemaError
	var staleDefinition *compiler.UnsupportedDefinitionError
	switch {
	case errors.Is(err, deploy.ErrEnvironmentNotFound),
		errors.Is(err, deploy.ErrRevisionNotFound),
		errors.Is(err, deploy.ErrDeploymentNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
	case errors.Is(err, deploy.ErrDeploymentInFlight):
		writeError(w, http.StatusConflict, codeDeploymentInFlight,
			"another deployment is already running for this environment")
	case errors.Is(err, deploy.ErrDestructiveChange):
		writeError(w, http.StatusConflict, codeDestructiveChange,
			"the plan contains destructive changes; review it and explicitly allow them")
	case errors.Is(err, deploy.ErrRegistryDisabled), errors.Is(err, registry.ErrDisabled):
		writeError(w, http.StatusServiceUnavailable, codeRegistryDisabled,
			"no managed registry is configured on this installation")
	case errors.Is(err, registry.ErrManifestNotFound):
		writeError(w, http.StatusConflict, codeDigestMismatch,
			"the registry does not hold the reported digest")
	case errors.Is(err, registry.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, codeRegistryUnavailable,
			"the managed registry did not answer")
	case errors.Is(err, deploy.ErrRevisionMismatch):
		writeError(w, http.StatusConflict, codeConflict, "the revision belongs to another environment")
	case errors.Is(err, deploy.ErrAlreadyTargeted):
		writeError(w, http.StatusConflict, codeConflict, "the environment already targets this revision")
	case errors.Is(err, deploy.ErrSourceEnvironmentNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "source environment not found")
	case errors.Is(err, deploy.ErrSameEnvironment):
		writeError(w, http.StatusBadRequest, codeBadRequest, trimDeployPrefix(err))
	case errors.Is(err, deploy.ErrSourceProjectMismatch):
		writeError(w, http.StatusUnprocessableEntity, codeBadRequest, trimDeployPrefix(err))
	case errors.Is(err, deploy.ErrNoActiveRevision):
		writeError(w, http.StatusConflict, codeConflict, "source environment has no active revision")
	case errors.Is(err, deploy.ErrInvalidDeploymentTransition):
		writeError(w, http.StatusConflict, codeConflict, trimDeployPrefix(err))
	case errors.Is(err, deploy.ErrDefinitionMismatch):
		writeError(w, http.StatusUnprocessableEntity, codeBadRequest, trimDeployPrefix(err))
	case errors.As(err, &capabilities):
		writeError(w, http.StatusUnprocessableEntity, codeUnsupportedCapabilities, trimDeployPrefix(err))
	case errors.As(err, &bucketPolicy):
		writeError(w, http.StatusUnprocessableEntity, codeBadRequest, trimDeployPrefix(err))
	case errors.As(err, &missingInput):
		writeError(w, http.StatusBadRequest, codeBadRequest, trimDeployPrefix(err))
	case errors.As(err, &platformMismatch):
		writeError(w, http.StatusUnprocessableEntity, codePlatformMismatch, trimDeployPrefix(err))
	case errors.As(err, &localsUnsupported), errors.As(err, &unknownLocal), errors.As(err, &invalidIntercept):
		writeError(w, http.StatusUnprocessableEntity, codeBadRequest, trimDeployPrefix(err))
	case errors.As(err, &invalidValues):
		writeError(w, http.StatusUnprocessableEntity, codeInvalidValues, invalidValues.Message)
	case errors.As(err, &incomplete):
		writeError(w, http.StatusConflict, codeArtifactsIncomplete, trimDeployPrefix(err))
	// 409, not 422: the request is well-formed; the conflict is with stored
	// state written by another build, and the remedy is a state transition.
	case errors.As(err, &staleRevision):
		writeError(w, http.StatusConflict, codeUnsupportedSchema, staleRevision.Error())
	case errors.As(err, &staleDefinition):
		writeError(w, http.StatusConflict, codeUnsupportedSchema, staleDefinition.Error())
	default:
		writeInternalError(ctx, w, "deploy error", err)
	}
}

func trimDeployPrefix(err error) string {
	message := err.Error()
	if after, found := strings.CutPrefix(message, "deploy: "); found {
		return after
	}
	return message
}

// shortDigestTag derives a stable human-scannable tag from an input hash.
func shortDigestTag(inputHash string) string {
	if len(inputHash) >= 12 {
		return "b" + inputHash[:12]
	}
	return "latest"
}
