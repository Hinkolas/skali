package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// --- product payload shapes (mirror api/openapi.yaml) ---

type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Access is the caller's standing: the project role and the effective
	// role per environment name (locked environments report none).
	Access ProjectAccess `json:"access"`
}

type ProjectAccess struct {
	Role         string            `json:"role"`
	Environments map[string]string `json:"environments"`
}

type Environment struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	// Access is the caller's effective role; "none" marks a locked
	// environment, which carries nothing else.
	Access    string               `json:"access"`
	CreatedAt *time.Time           `json:"created_at,omitempty"`
	Settings  *EnvironmentSettings `json:"settings,omitempty"`
}

// Locked: the caller may see the environment's name and nothing inside.
func (e Environment) Locked() bool { return e.Access == "none" }

type EnvironmentSettings struct {
	MaxRole      string   `json:"max_role"`
	DeployPolicy string   `json:"deploy_policy"`
	PromoteFrom  []string `json:"promote_from"`
	Priority     string   `json:"priority"`
}

// EnvironmentSettingsPatch is a partial settings update; nil fields keep
// their value.
type EnvironmentSettingsPatch struct {
	MaxRole      *string   `json:"max_role,omitempty"`
	DeployPolicy *string   `json:"deploy_policy,omitempty"`
	PromoteFrom  *[]string `json:"promote_from,omitempty"`
	Priority     *string   `json:"priority,omitempty"`
}

// Member is one user's role on a project (membership) or on an environment
// (cell). The members listing also carries InstanceAdmin and the effective
// role per environment name (only environments the caller may read).
type Member struct {
	UserID        string                       `json:"user_id"`
	Email         string                       `json:"email"`
	Name          string                       `json:"name"`
	Role          string                       `json:"role"`
	InstanceAdmin bool                         `json:"instance_admin"`
	Environments  map[string]MemberEnvironment `json:"environments"`
}

// MemberEnvironment is one grid cell: the effective role and the explicit
// per-environment role when one exists (empty when inherited).
type MemberEnvironment struct {
	Role string  `json:"role"`
	Cell *string `json:"cell"`
}

type DefinitionVersion struct {
	DefinitionVersionID string `json:"definition_version_id"`
	DefinitionHash      string `json:"definition_hash"`
}

type StagedValues struct {
	CandidateID string   `json:"candidate_id"`
	Staged      []string `json:"staged"`
	// Skipped lists submitted names the definition does not reference;
	// they were not staged.
	Skipped []string `json:"skipped"`
}

type BuildInput struct {
	InputHash  string `json:"input_hash"`
	ConfigHash string `json:"config_hash,omitempty"`
	Platform   string `json:"platform,omitempty"`
}

type PlanChange struct {
	Service     string `json:"service"`
	Action      string `json:"action"`
	Destructive bool   `json:"destructive,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

type PlanValueChange struct {
	Name   string `json:"name"`
	Action string `json:"action"`
}

type PlanDocument struct {
	Project string            `json:"project"`
	Changes []PlanChange      `json:"changes"`
	Values  []PlanValueChange `json:"values"`
}

func (p *PlanDocument) Empty() bool {
	return p == nil || (len(p.Changes) == 0 && len(p.Values) == 0)
}

func (p *PlanDocument) Destructive() bool {
	if p == nil {
		return false
	}
	for _, change := range p.Changes {
		if change.Destructive {
			return true
		}
	}
	return false
}

type ArtifactAction struct {
	Application string `json:"application"`
	Action      string `json:"action"`
	Kind        string `json:"kind"`
	ArtifactID  string `json:"artifact_id"`
	BuildID     string `json:"build_id"`
	Upstream    string `json:"upstream"`
	Platform    string `json:"platform"`
	Reference   string `json:"reference"`
	Digest      string `json:"digest"`
	PushRef     string `json:"push_ref"`
	StepKey     string `json:"step_key"`
}

type PlanResult struct {
	Plan     *PlanDocument    `json:"plan"`
	Actions  []ArtifactAction `json:"actions"`
	UpToDate bool             `json:"up_to_date"`
	// Orphaned lists stored value names the definition no longer
	// references; deployments ignore them. Advisory only.
	Orphaned []string `json:"orphaned"`
	// VolumeSizesUnenforced: the manifest declares volumes but the target
	// cluster's storage driver enforces no sizes. Advisory only.
	VolumeSizesUnenforced bool `json:"volume_sizes_unenforced"`
	// RequiredRole is the environment role this deploy needs (deploy for
	// code-only, maintain when it changes the definition or values).
	RequiredRole string `json:"required_role"`
	// BypassProtection reports that the server consumed an explicit bypass
	// of the environment's promote-only policy for this request.
	BypassProtection bool `json:"bypass_protection"`
}

type Deployment struct {
	ID                  string `json:"id"`
	ProjectID           string `json:"project_id"`
	EnvironmentID       string `json:"environment_id"`
	DefinitionVersionID string `json:"definition_version_id"`
	Status              string `json:"status"`
	RevisionID          string `json:"revision_id"`
	RunID               string `json:"run_id"`
	BuildExecutor       string `json:"build_executor"`
	BypassProtection    bool   `json:"bypass_protection"`
}

type OpenedDeployment struct {
	Deployment            *Deployment      `json:"deployment"`
	Plan                  *PlanDocument    `json:"plan"`
	Actions               []ArtifactAction `json:"actions"`
	UpToDate              bool             `json:"up_to_date"`
	Orphaned              []string         `json:"orphaned"`
	VolumeSizesUnenforced bool             `json:"volume_sizes_unenforced"`
	RequiredRole          string           `json:"required_role"`
	BypassProtection      bool             `json:"bypass_protection"`
}

type CompletedDeployment struct {
	RunID      string `json:"run_id"`
	RevisionID string `json:"revision_id"`
}

type VerifiedArtifact struct {
	ID        string `json:"id"`
	Phase     string `json:"phase"`
	Reference string `json:"reference"`
	Digest    string `json:"digest"`
}

type Run struct {
	ID            string     `json:"id"`
	Kind          string     `json:"kind"`
	EnvironmentID *string    `json:"environment_id"`
	Actor         string     `json:"actor"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	StartedAt     *time.Time `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at"`
	// BypassProtection marks a deployment that entered a promote-only
	// environment on an environment admin's explicit bypass.
	BypassProtection bool `json:"bypass_protection"`
}

type Attempt struct {
	ID         string     `json:"id"`
	Number     int64      `json:"number"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

type Step struct {
	ID         string     `json:"id"`
	Key        string     `json:"key"`
	Title      string     `json:"title"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Attempts   []Attempt  `json:"attempts"`
	Children   []Step     `json:"children"`
}

type RunTree struct {
	Run   Run    `json:"run"`
	Steps []Step `json:"steps"`
}

type LogEntry struct {
	Attempt int64     `json:"attempt"`
	Seq     int64     `json:"seq"`
	TS      time.Time `json:"ts"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

type Target struct {
	TargetRevisionID *string   `json:"target_revision_id"`
	ActiveRevisionID *string   `json:"active_revision_id"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type RevisionSummary struct {
	ID                  string    `json:"id"`
	ProjectID           string    `json:"project_id"`
	EnvironmentID       string    `json:"environment_id"`
	DefinitionVersionID string    `json:"definition_version_id"`
	SchemaVersion       string    `json:"schema_version"`
	Checksum            string    `json:"checksum"`
	DefinitionHash      string    `json:"definition_hash"`
	ValuesHash          string    `json:"values_hash"`
	CompilerVersion     string    `json:"compiler_version"`
	CreatedAt           time.Time `json:"created_at"`
}

// --- projects and environments ---

func (c *Client) CreateProject(ctx context.Context, name string) (*Project, error) {
	var res struct {
		Project Project `json:"project"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/projects", map[string]string{"name": name}, &res); err != nil {
		return nil, err
	}
	return &res.Project, nil
}

func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	var res struct {
		Projects []Project `json:"projects"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/projects", nil, &res); err != nil {
		return nil, err
	}
	return res.Projects, nil
}

// CreateEnvironment creates an environment; priority is normal or high
// (empty means normal; high needs an instance admin).
func (c *Client) CreateEnvironment(ctx context.Context, projectID, name, priority string) (*Environment, error) {
	var res struct {
		Environment Environment `json:"environment"`
	}
	body := map[string]string{"name": name}
	if priority != "" {
		body["priority"] = priority
	}
	err := c.do(ctx, http.MethodPost, "/v1/projects/"+projectID+"/environments", body, &res)
	if err != nil {
		return nil, err
	}
	return &res.Environment, nil
}

// UpdateEnvironmentSettings patches environment settings. Requires a fresh
// session and environment admin.
func (c *Client) UpdateEnvironmentSettings(ctx context.Context, environmentID string, patch EnvironmentSettingsPatch) (*Environment, error) {
	var res struct {
		Environment Environment `json:"environment"`
	}
	if err := c.do(ctx, http.MethodPatch, "/v1/environments/"+environmentID, patch, &res); err != nil {
		return nil, err
	}
	return &res.Environment, nil
}

// --- members and cells ---

func (c *Client) ListMembers(ctx context.Context, projectID string) ([]Member, error) {
	var res struct {
		Members []Member `json:"members"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/projects/"+projectID+"/members", nil, &res); err != nil {
		return nil, err
	}
	return res.Members, nil
}

// SetMember adds or changes a membership; user is an id or an email.
// Requires a fresh session and project admin.
func (c *Client) SetMember(ctx context.Context, projectID, user, role string) (*Member, error) {
	var res struct {
		Member Member `json:"member"`
	}
	err := c.do(ctx, http.MethodPut, "/v1/projects/"+projectID+"/members/"+url.PathEscape(user),
		map[string]string{"role": role}, &res)
	if err != nil {
		return nil, err
	}
	return &res.Member, nil
}

func (c *Client) RemoveMember(ctx context.Context, projectID, user string) error {
	return c.do(ctx, http.MethodDelete, "/v1/projects/"+projectID+"/members/"+url.PathEscape(user), nil, nil)
}

func (c *Client) ListEnvironmentAccess(ctx context.Context, environmentID string) ([]Member, error) {
	var res struct {
		Access []Member `json:"access"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/environments/"+environmentID+"/access", nil, &res); err != nil {
		return nil, err
	}
	return res.Access, nil
}

// SetEnvironmentAccess sets a cell; user is an id or an email. Requires a
// fresh session and environment admin.
func (c *Client) SetEnvironmentAccess(ctx context.Context, environmentID, user, role string) (*Member, error) {
	var res struct {
		Access Member `json:"access"`
	}
	err := c.do(ctx, http.MethodPut, "/v1/environments/"+environmentID+"/access/"+url.PathEscape(user),
		map[string]string{"role": role}, &res)
	if err != nil {
		return nil, err
	}
	return &res.Access, nil
}

func (c *Client) RemoveEnvironmentAccess(ctx context.Context, environmentID, user string) error {
	return c.do(ctx, http.MethodDelete, "/v1/environments/"+environmentID+"/access/"+url.PathEscape(user), nil, nil)
}

func (c *Client) ListEnvironments(ctx context.Context, projectID string) ([]Environment, error) {
	var res struct {
		Environments []Environment `json:"environments"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/projects/"+projectID+"/environments", nil, &res); err != nil {
		return nil, err
	}
	return res.Environments, nil
}

func (c *Client) GetEnvironment(ctx context.Context, environmentID string) (*Environment, error) {
	var res struct {
		Environment Environment `json:"environment"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/environments/"+environmentID, nil, &res); err != nil {
		return nil, err
	}
	return &res.Environment, nil
}

// TeardownEnvironment persists the destructive teardown decision on the
// server and returns the run to attach to. Requires a fresh session.
func (c *Client) TeardownEnvironment(ctx context.Context, environmentID string, purge bool) (runID string, err error) {
	var res struct {
		RunID string `json:"run_id"`
	}
	err = c.do(ctx, http.MethodPost, "/v1/environments/"+environmentID+"/teardown",
		map[string]bool{"purge": purge}, &res)
	if err != nil {
		return "", err
	}
	return res.RunID, nil
}

// Reauthenticate refreshes the session's recent-authentication window with
// the account password, unlocking the destructive endpoints.
func (c *Client) Reauthenticate(ctx context.Context, password string) error {
	return c.do(ctx, http.MethodPost, "/v1/auth/reauth", map[string]string{"password": password}, nil)
}

// ReauthenticateWithCode is the second-factor variant for accounts with
// two-factor authentication enrolled, which the server requires over the
// password.
func (c *Client) ReauthenticateWithCode(ctx context.Context, code string) error {
	return c.do(ctx, http.MethodPost, "/v1/auth/reauth", map[string]string{"code": code}, nil)
}

// --- deployment flow ---

func (c *Client) SubmitDefinition(ctx context.Context, projectID, source, format string) (*DefinitionVersion, error) {
	var res DefinitionVersion
	err := c.do(ctx, http.MethodPost, "/v1/projects/"+projectID+"/definitions",
		map[string]string{"source": source, "format": format}, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) StageValues(ctx context.Context, environmentID string, values map[string]string, definitionVersionID string) (*StagedValues, error) {
	var res StagedValues
	body := map[string]any{"values": values}
	if definitionVersionID != "" {
		body["definition_version_id"] = definitionVersionID
	}
	if err := c.do(ctx, http.MethodPut, "/v1/environments/"+environmentID+"/values", body, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

type DeployRequest struct {
	DefinitionVersionID string `json:"definition_version_id,omitempty"`
	// FromEnvironmentID promotes the source environment's active revision
	// instead of a submitted definition; exactly one of the two is set.
	FromEnvironmentID string                `json:"from_environment_id,omitempty"`
	CandidateID       string                `json:"candidate_id,omitempty"`
	BuildExecutor     string                `json:"build_executor,omitempty"`
	AllowDestructive  bool                  `json:"allow_destructive,omitempty"`
	Builds            map[string]BuildInput `json:"builds,omitempty"`
	// Force deploys even when the environment is up to date; application
	// workloads restart at promotion.
	Force bool `json:"force,omitempty"`
	// Rebuild ignores artifact reuse: builds run again and images
	// re-import, picking up moved upstream tags and refreshed bases.
	Rebuild bool `json:"rebuild,omitempty"`
	// LocalApplications declares applications this dev session runs on the
	// host: no build input, no artifact; the server intercepts their
	// Service to the declared host ports. Each deploy replaces the
	// environment's intercept set, so an absent map clears it.
	LocalApplications map[string]LocalApplication `json:"local_applications,omitempty"`
	// PruneValues removes the stored values the definition no longer
	// references as part of the deployment; they show as prune rows in the
	// plan instead of the orphaned advisory.
	PruneValues bool `json:"prune_values,omitempty"`
	// BypassProtection asks to deploy into a promote-only environment
	// anyway; the server consumes it only when the policy would refuse and
	// then requires environment admin and a fresh session.
	BypassProtection bool `json:"bypass_protection,omitempty"`
}

// LocalApplication maps an application's manifest port names to the host
// ports its local dev process listens on.
type LocalApplication struct {
	Ports map[string]int `json:"ports,omitempty"`
}

func (c *Client) Plan(ctx context.Context, environmentID string, req DeployRequest) (*PlanResult, error) {
	var res PlanResult
	body := map[string]any{}
	if req.DefinitionVersionID != "" {
		body["definition_version_id"] = req.DefinitionVersionID
	}
	if req.FromEnvironmentID != "" {
		body["from_environment_id"] = req.FromEnvironmentID
	}
	if req.CandidateID != "" {
		body["candidate_id"] = req.CandidateID
	}
	if len(req.Builds) > 0 {
		body["builds"] = req.Builds
	}
	if req.Rebuild {
		body["rebuild"] = true
	}
	if len(req.LocalApplications) > 0 {
		body["local_applications"] = req.LocalApplications
	}
	if req.PruneValues {
		body["prune_values"] = true
	}
	// Sent only when set: skalid rejects unknown fields, and a newer CLI
	// must keep planning against an older installation.
	if req.BypassProtection {
		body["bypass_protection"] = true
	}
	if err := c.do(ctx, http.MethodPost, "/v1/environments/"+environmentID+"/plan", body, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) OpenDeployment(ctx context.Context, environmentID string, req DeployRequest) (*OpenedDeployment, error) {
	var res OpenedDeployment
	if err := c.do(ctx, http.MethodPost, "/v1/environments/"+environmentID+"/deployments", req, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) GetDeployment(ctx context.Context, id string) (*OpenedDeployment, error) {
	var res OpenedDeployment
	if err := c.do(ctx, http.MethodGet, "/v1/deployments/"+id, nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) CompleteDeployment(ctx context.Context, id string) (*CompletedDeployment, error) {
	var res CompletedDeployment
	if err := c.do(ctx, http.MethodPost, "/v1/deployments/"+id+"/complete", nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) FailDeployment(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/deployments/"+id+"/fail", nil, nil)
}

func (c *Client) VerifyArtifact(ctx context.Context, artifactID, deploymentID, digest string) (*VerifiedArtifact, error) {
	var res VerifiedArtifact
	err := c.do(ctx, http.MethodPost, "/v1/artifacts/"+artifactID+"/verify",
		map[string]string{"deployment_id": deploymentID, "digest": digest}, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) HeartbeatBuild(ctx context.Context, buildID string) (bool, error) {
	var res struct {
		Alive bool `json:"alive"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/builds/"+buildID+"/heartbeat", nil, &res); err != nil {
		return false, err
	}
	return res.Alive, nil
}

// --- runs and client steps ---

func (c *Client) ListRuns(ctx context.Context, environmentID string) ([]Run, error) {
	var res struct {
		Runs []Run `json:"runs"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/environments/"+environmentID+"/runs", nil, &res); err != nil {
		return nil, err
	}
	return res.Runs, nil
}

func (c *Client) GetRun(ctx context.Context, id string) (*RunTree, error) {
	var res RunTree
	if err := c.do(ctx, http.MethodGet, "/v1/runs/"+id, nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) CancelRun(ctx context.Context, id string) (bool, error) {
	var res struct {
		Status   string `json:"status"`
		Fallback bool   `json:"fallback"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/runs/"+id+"/cancel", nil, &res); err != nil {
		return false, err
	}
	return res.Fallback, nil
}

func (c *Client) EnsureStep(ctx context.Context, runID, key, title, parentKey string) (*Step, error) {
	var res Step
	err := c.do(ctx, http.MethodPost, "/v1/runs/"+runID+"/steps",
		map[string]string{"key": key, "title": title, "parent_key": parentKey}, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) SetStepStatus(ctx context.Context, stepID, status string) error {
	return c.do(ctx, http.MethodPatch, "/v1/steps/"+stepID, map[string]string{"status": status}, nil)
}

type LogLine struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

func (c *Client) AppendStepLogs(ctx context.Context, stepID string, lines []LogLine) error {
	return c.do(ctx, http.MethodPost, "/v1/steps/"+stepID+"/logs", map[string]any{"lines": lines}, nil)
}

func (c *Client) StepLogs(ctx context.Context, stepID, after string, limit int) ([]LogEntry, string, error) {
	var res struct {
		Logs []LogEntry `json:"logs"`
		Next string     `json:"next"`
	}
	path := "/v1/steps/" + stepID + "/logs"
	if after != "" {
		path += "?after=" + after
	}
	_ = limit
	if err := c.do(ctx, http.MethodGet, path, nil, &res); err != nil {
		return nil, "", err
	}
	return res.Logs, res.Next, nil
}

func (c *Client) Target(ctx context.Context, environmentID string) (*Target, error) {
	var res struct {
		Target Target `json:"target"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/environments/"+environmentID+"/target", nil, &res); err != nil {
		return nil, err
	}
	return &res.Target, nil
}

func (c *Client) ListRevisions(ctx context.Context, environmentID string) ([]RevisionSummary, error) {
	var res struct {
		Revisions []RevisionSummary `json:"revisions"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/environments/"+environmentID+"/revisions", nil, &res); err != nil {
		return nil, err
	}
	return res.Revisions, nil
}

// SetTargetResult is the rollback response: the moved pointer pair and the
// run that carries the rollout.
type SetTargetResult struct {
	Target Target `json:"target"`
	RunID  string `json:"run_id"`
}

// SetTarget rolls the environment back to an existing revision.
func (c *Client) SetTarget(ctx context.Context, environmentID, revisionID string) (*SetTargetResult, error) {
	var res SetTargetResult
	err := c.do(ctx, http.MethodPut, "/v1/environments/"+environmentID+"/target",
		map[string]string{"revision_id": revisionID}, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// EnvironmentStatus is the topology and health projection; the CLI shows
// it in dev status and after rollouts.
type EnvironmentStatus struct {
	EnvironmentID  string `json:"environment_id"`
	State          string `json:"state"`
	TargetRevision *struct {
		ID       string `json:"id"`
		Checksum string `json:"checksum"`
	} `json:"target_revision"`
	ActiveRevision *struct {
		ID       string `json:"id"`
		Checksum string `json:"checksum"`
	} `json:"active_revision"`
	Observation struct {
		State string `json:"state"`
	} `json:"observation"`
	// Platforms lists the observed platforms of the cluster's application
	// nodes; nil when the server predates the field or the observation has
	// not synced.
	Platforms []string `json:"platforms"`
	// PlatformPreference is the cluster's ordered build platform
	// preference; nil when the server predates the field or none is
	// configured.
	PlatformPreference []string        `json:"platform_preference"`
	Services           []ServiceStatus `json:"services"`
}

type ServiceStatus struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	Health      string `json:"health"`
	Intercepted bool   `json:"intercepted,omitempty"`
	Diagnostics []struct {
		Severity string `json:"severity"`
		Code     string `json:"code"`
		Message  string `json:"message"`
	} `json:"diagnostics"`
	Pods []struct {
		Name  string `json:"name"`
		Ready bool   `json:"ready"`
		Phase string `json:"phase"`
	} `json:"pods"`
	Routes []RouteStatus `json:"routes,omitempty"`
}

// RouteStatus is one public route with its edge policies; Certificate is
// nil where none exists by design (tls disabled, local installation).
type RouteStatus struct {
	Key         string             `json:"key"`
	Domain      string             `json:"domain"`
	Path        string             `json:"path"`
	TLS         string             `json:"tls"`
	Strategy    string             `json:"strategy"`
	Certificate *CertificateStatus `json:"certificate,omitempty"`
}

type CertificateStatus struct {
	Name        string     `json:"name"`
	SecretName  string     `json:"secret_name"`
	State       string     `json:"state"` // pending | issuing | active | failing | expired
	Reason      string     `json:"reason,omitempty"`
	Message     string     `json:"message,omitempty"`
	NotAfter    *time.Time `json:"not_after"`
	RenewalTime *time.Time `json:"renewal_time"`
}

// ValueEntry is one stored environment value; values are write-only, so an
// entry carries the name and version alone.
type ValueEntry struct {
	Name    string `json:"name"`
	Version int64  `json:"version"`
}

func (c *Client) EnvironmentValues(ctx context.Context, environmentID string) ([]ValueEntry, error) {
	var res struct {
		Values []ValueEntry `json:"values"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/environments/"+environmentID+"/values", nil, &res); err != nil {
		return nil, err
	}
	return res.Values, nil
}

func (c *Client) EnvironmentStatus(ctx context.Context, environmentID string) (*EnvironmentStatus, error) {
	var res EnvironmentStatus
	if err := c.do(ctx, http.MethodGet, "/v1/environments/"+environmentID+"/status", nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// ResolvedEnvironment is one application's fully resolved variable set: what
// its pod would receive, with endpoint-bearing outputs rewritten to the
// host's loopback ports. Variables whose services are not provisioned are
// omitted with a warning.
type ResolvedEnvironment struct {
	Values   map[string]string `json:"values"`
	Warnings []string          `json:"warnings"`
}

// ApplicationEnvironment resolves an application's environment for host-run
// processes (skali dev). Requires fresh authentication like credential
// reveal; portBase is the first host port of the CLI's loopback range.
func (c *Client) ApplicationEnvironment(ctx context.Context, environmentID, applicationKey string, portBase int) (*ResolvedEnvironment, error) {
	var res ResolvedEnvironment
	path := "/v1/environments/" + environmentID + "/applications/" + applicationKey +
		"/environment?audience=local&port_base=" + strconv.Itoa(portBase)
	if err := c.do(ctx, http.MethodGet, path, nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// BackupTarget is a backup location as the API reports it: everything
// except the secret access key, which is write-only.
type BackupTarget struct {
	Name        string `json:"name"`
	Endpoint    string `json:"endpoint"`
	Region      string `json:"region"`
	Bucket      string `json:"bucket"`
	Prefix      string `json:"prefix"`
	AccessKeyID string `json:"access_key_id"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// BackupTargetInput is one complete backup target write; there are no
// partial updates.
type BackupTargetInput struct {
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

func (c *Client) GetBackupTarget(ctx context.Context) (*BackupTarget, error) {
	var res struct {
		Target BackupTarget `json:"target"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/system/backup-target", nil, &res); err != nil {
		return nil, err
	}
	return &res.Target, nil
}

func (c *Client) PutBackupTarget(ctx context.Context, input BackupTargetInput) (*BackupTarget, error) {
	var res struct {
		Target BackupTarget `json:"target"`
	}
	if err := c.do(ctx, http.MethodPut, "/v1/system/backup-target", input, &res); err != nil {
		return nil, err
	}
	return &res.Target, nil
}

// DeleteBackupTarget removes the configured backup target and its stored
// credentials. Snapshots already written to the bucket stay untouched.
func (c *Client) DeleteBackupTarget(ctx context.Context) error {
	return c.do(ctx, http.MethodDelete, "/v1/system/backup-target", nil, nil)
}

// BackupSnapshot is one listable snapshot, read from its S3 manifest.
type BackupSnapshot struct {
	ID               string `json:"id"`
	Environment      string `json:"environment"`
	CreatedAt        string `json:"created_at"`
	RevisionChecksum string `json:"revision_checksum"`
	Encryption       string `json:"encryption"`
	Databases        int    `json:"databases"`
	Buckets          int    `json:"buckets"`
	Volumes          int    `json:"volumes"`
	Bytes            int64  `json:"bytes"`
}

// CreateBackupResult identifies the accepted backup operation.
type CreateBackupResult struct {
	RunID    string `json:"run_id"`
	BackupID string `json:"backup_id"`
}

// CreateBackup requests a manual snapshot of the environment's data.
func (c *Client) CreateBackup(ctx context.Context, environmentID string) (*CreateBackupResult, error) {
	var res CreateBackupResult
	if err := c.do(ctx, http.MethodPost, "/v1/environments/"+environmentID+"/backups", nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// ListBackups returns the environment's snapshots, newest first.
func (c *Client) ListBackups(ctx context.Context, environmentID string) ([]BackupSnapshot, error) {
	var res struct {
		Snapshots []BackupSnapshot `json:"snapshots"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/environments/"+environmentID+"/backups", nil, &res); err != nil {
		return nil, err
	}
	return res.Snapshots, nil
}

// ListProjectBackups returns the snapshots of every environment of the
// project, newest first.
func (c *Client) ListProjectBackups(ctx context.Context, projectID string) ([]BackupSnapshot, error) {
	var res struct {
		Snapshots []BackupSnapshot `json:"snapshots"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/projects/"+projectID+"/backups", nil, &res); err != nil {
		return nil, err
	}
	return res.Snapshots, nil
}

// RestoreBackup requests a stop-first restore of one of the project's
// snapshots into the environment; the environment stops, data is replaced,
// and the current revision resumes.
func (c *Client) RestoreBackup(ctx context.Context, environmentID, snapshotID string) (string, error) {
	var res struct {
		RunID string `json:"run_id"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/environments/"+environmentID+"/restore",
		map[string]string{"snapshot_id": snapshotID}, &res)
	if err != nil {
		return "", err
	}
	return res.RunID, nil
}
