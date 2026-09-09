package clusterstate

import "time"

type HostFacts struct {
	InstallationID string   `json:"installationId"`
	NodeID         string   `json:"nodeId,omitempty"`
	Name           string   `json:"name"`
	NodeIP         string   `json:"nodeIP,omitempty"`
	Capabilities   []string `json:"capabilities"`
	AgentVersion   string   `json:"agentVersion"`
}

type PreflightRequest struct {
	Host HostFacts `json:"host"`
}

type PreflightResponse struct {
	AllowedCapabilities []string `json:"allowedCapabilities,omitempty"`
	Cluster             string   `json:"cluster"`
	Role                string   `json:"role"`
	Coordinators        []string `json:"coordinators"`
}

type EnrollRequest struct {
	Host HostFacts `json:"host"`
	CSR  string    `json:"csr"`
}

type EnrollResponse struct {
	Cluster      string   `json:"cluster"`
	Role         string   `json:"role"`
	NodeID       string   `json:"nodeId"`
	Certificate  string   `json:"certificate"`
	CACert       string   `json:"caCert"`
	Coordinators []string `json:"coordinators"`
}

type AgentReport struct {
	Phase      string `json:"phase"`
	K3sVersion string `json:"k3sVersion,omitempty"`
	// AgentVersion is the running skali-hostd build, so the coordinator
	// sees a node pick up a new binary after an upgrade action.
	AgentVersion string `json:"agentVersion,omitempty"`
	ActionID     string `json:"actionId,omitempty"`
	ActionOK     bool   `json:"actionOk,omitempty"`
	Error        string `json:"error,omitempty"`
}

type AgentPollRequest struct {
	Report AgentReport `json:"report"`
}

type AgentAction struct {
	RestartCoordinator bool     `json:"restartCoordinator,omitempty"`
	ID                 string   `json:"id,omitempty"`
	Type               string   `json:"type,omitempty"`
	Cluster            string   `json:"cluster,omitempty"`
	Role               string   `json:"role,omitempty"`
	NodeName           string   `json:"nodeName,omitempty"`
	NodeIP             string   `json:"nodeIP,omitempty"`
	Capabilities       []string `json:"capabilities,omitempty"`
	Server             string   `json:"server,omitempty"`
	K3sToken           string   `json:"k3sToken,omitempty"`
	PullSecret         string   `json:"pullSecret,omitempty"`
	// Version names the release an upgrade action moves the node to; the
	// agent derives every download from it and the fixed release host, so
	// no URL and no command ever travels in an action. HostdSHA256 is the
	// published checksum of skali-hostd for this node's architecture, read
	// by the coordinator from the release's checksums.txt.
	Version     string `json:"version,omitempty"`
	HostdSHA256 string `json:"hostdSha256,omitempty"`
}

type AgentPollResponse struct {
	Action            AgentAction   `json:"action"`
	Coordinators      []string      `json:"coordinators,omitempty"`
	ConvergedRevision string        `json:"convergedRevision,omitempty"`
	TargetRevision    string        `json:"targetRevision,omitempty"`
	CandidateRevision string        `json:"candidateRevision,omitempty"`
	OperationID       string        `json:"operationId,omitempty"`
	OperationPhase    string        `json:"operationPhase,omitempty"`
	RetryAfter        time.Duration `json:"retryAfter"`
}

type RenewRequest struct {
	CSR string `json:"csr"`
}

type RenewResponse struct {
	Certificate string `json:"certificate"`
}
