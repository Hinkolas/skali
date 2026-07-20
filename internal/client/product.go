package client

import (
	"context"
	"net/http"
	"time"
)

// --- product payload shapes (mirror api/openapi.yaml) ---

type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Environment struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
}

type DefinitionVersion struct {
	DefinitionVersionID string `json:"definition_version_id"`
	DefinitionHash      string `json:"definition_hash"`
}

type StagedValues struct {
	CandidateID string   `json:"candidate_id"`
	Plain       []string `json:"plain"`
	Secret      []string `json:"secret"`
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
	Secret bool   `json:"secret,omitempty"`
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
}

type OpenedDeployment struct {
	Deployment *Deployment      `json:"deployment"`
	Plan       *PlanDocument    `json:"plan"`
	Actions    []ArtifactAction `json:"actions"`
	UpToDate   bool             `json:"up_to_date"`
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
	EnvironmentID    string  `json:"environment_id"`
	TargetRevisionID *string `json:"target_revision_id"`
	ActiveRevisionID *string `json:"active_revision_id"`
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

func (c *Client) CreateEnvironment(ctx context.Context, projectID, name string) (*Environment, error) {
	var res struct {
		Environment Environment `json:"environment"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/projects/"+projectID+"/environments",
		map[string]string{"name": name}, &res)
	if err != nil {
		return nil, err
	}
	return &res.Environment, nil
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
	DefinitionVersionID string                `json:"definition_version_id"`
	CandidateID         string                `json:"candidate_id,omitempty"`
	BuildExecutor       string                `json:"build_executor,omitempty"`
	AllowDestructive    bool                  `json:"allow_destructive,omitempty"`
	Builds              map[string]BuildInput `json:"builds,omitempty"`
}

func (c *Client) Plan(ctx context.Context, environmentID string, req DeployRequest) (*PlanResult, error) {
	var res PlanResult
	body := map[string]any{"definition_version_id": req.DefinitionVersionID}
	if req.CandidateID != "" {
		body["candidate_id"] = req.CandidateID
	}
	if len(req.Builds) > 0 {
		body["builds"] = req.Builds
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
	var res Target
	if err := c.do(ctx, http.MethodGet, "/v1/environments/"+environmentID+"/target", nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// EnvironmentStatus is the topology and health projection; the CLI shows
// it in dev status and after rollouts.
type EnvironmentStatus struct {
	EnvironmentID  string `json:"environment_id"`
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
	Services []ServiceStatus `json:"services"`
}

type ServiceStatus struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	Health      string `json:"health"`
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
}

// ValueEntry is one stored environment value; secret entries never carry
// their value.
type ValueEntry struct {
	Name    string `json:"name"`
	Secret  bool   `json:"secret"`
	Version int64  `json:"version"`
	Value   string `json:"value"`
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
