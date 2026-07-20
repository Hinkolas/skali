package module

// Health is the projected condition of one service, derived purely from
// prepared intent plus observed state (REWORK_V2 section 7.5).
type Health string

const (
	HealthUnknown     Health = "unknown"
	HealthProgressing Health = "progressing"
	HealthHealthy     Health = "healthy"
	HealthDegraded    Health = "degraded"
	HealthUnhealthy   Health = "unhealthy"
)

// Diagnostic is one structured observation attached to an evaluation.
type Diagnostic struct {
	Severity string `json:"severity"` // info | warning | error
	Code     string `json:"code"`
	Message  string `json:"message"`
	Resource string `json:"resource,omitempty"`
}

// Evaluation is the result of one pure health evaluation.
type Evaluation struct {
	Health      Health
	Diagnostics []Diagnostic
}

// ObservedResource is the R1 stand-in for the R2 ObservedStore projection:
// just enough typed shape for pure health evaluation. R2 replaces the
// Fields map with typed projections without changing Evaluate's contract.
type ObservedResource struct {
	Kind   string
	Name   string
	Fields map[string]int64
}
