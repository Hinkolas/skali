// Package compiler converts a human-authored manifest into Skali's canonical,
// target-independent project definition.
package compiler

type Result struct {
	Hash       string            "json:\"hash\""
	Definition ProjectDefinition "json:\"definition\""
}

type ProjectDefinition struct {
	Version           string                   "json:\"version\""
	Name              string                   "json:\"name\""
	Description       string                   "json:\"description,omitempty\""
	Applications      map[string]Application   "json:\"applications,omitempty\""
	Databases         map[string]DatabaseClaim "json:\"databases,omitempty\""
	Buckets           map[string]BucketClaim   "json:\"buckets,omitempty\""
	Backups           map[string]Backup        "json:\"backups,omitempty\""
	RequiredVariables []VariableRequirement    "json:\"requiredVariables,omitempty\""
	Dependencies      map[string][]string      "json:\"dependencies,omitempty\""
}

// VariableRequirement is the compiled contract for one ${NAME} project value,
// derived entirely from references. Runtime marks names the server resolves at
// render time; Build marks names the client resolves locally for image builds.
// A name referenced in both positions carries both flags.
type VariableRequirement struct {
	Name       string "json:\"name\""
	Required   bool   "json:\"required\""
	Default    string "json:\"default,omitempty\""
	HasDefault bool   "json:\"hasDefault,omitempty\""
	Runtime    bool   "json:\"runtime,omitempty\""
	Build      bool   "json:\"build,omitempty\""
}

type Expression struct {
	Parts []ExpressionPart "json:\"parts\""
}

type ExpressionPart struct {
	Kind       string "json:\"kind\""
	Value      string "json:\"value,omitempty\""
	Name       string "json:\"name,omitempty\""
	Default    string "json:\"default,omitempty\""
	HasDefault bool   "json:\"hasDefault,omitempty\""
	Collection string "json:\"collection,omitempty\""
	Service    string "json:\"service,omitempty\""
	Output     string "json:\"output,omitempty\""
	Sensitive  bool   "json:\"sensitive,omitempty\""
}

type Application struct {
	Source      ApplicationSource     "json:\"source\""
	Command     []Expression          "json:\"command,omitempty\""
	Environment map[string]Expression "json:\"environment,omitempty\""
	Ports       map[string]Port       "json:\"ports,omitempty\""
	Routes      map[string]Route      "json:\"routes,omitempty\""
	Health      Health                "json:\"health,omitzero\""
	Resources   Resources             "json:\"resources,omitzero\""
	Scaling     Scaling               "json:\"scaling\""
	Placement   Placement             "json:\"placement,omitzero\""
	Deployment  Deployment            "json:\"deployment,omitzero\""
	Shutdown    Shutdown              "json:\"shutdown,omitempty\""
	Volumes     map[string]Volume     "json:\"volumes,omitempty\""
}

type ApplicationSource struct {
	Kind  string     "json:\"kind\""
	Image Expression "json:\"image,omitzero\""
	Build Build      "json:\"build,omitzero\""
}

type Build struct {
	Context    string                "json:\"context\""
	Dockerfile string                "json:\"dockerfile\""
	Target     Expression            "json:\"target,omitzero\""
	Arguments  map[string]Expression "json:\"arguments,omitempty\""
}

type Port struct {
	Port     int    "json:\"port\""
	Protocol string "json:\"protocol\""
}

type PortTarget struct {
	Name   string "json:\"name,omitempty\""
	Number int    "json:\"number,omitempty\""
}

type Route struct {
	Domain Expression "json:\"domain\""
	Path   Expression "json:\"path\""
	Port   PortTarget "json:\"port\""
	TLS    string     "json:\"tls\""
}

type Health struct {
	Startup   Probe "json:\"startup,omitzero\""
	Readiness Probe "json:\"readiness,omitzero\""
	Liveness  Probe "json:\"liveness,omitzero\""
}

type Probe struct {
	HTTP             HTTPProbe "json:\"http,omitzero\""
	IntervalMillis   int64     "json:\"intervalMillis,omitempty\""
	TimeoutMillis    int64     "json:\"timeoutMillis,omitempty\""
	FailureThreshold int       "json:\"failureThreshold,omitempty\""
}

type HTTPProbe struct {
	Port PortTarget "json:\"port\""
	Path Expression "json:\"path\""
}

type Resources struct {
	Requests ResourceValues "json:\"requests,omitzero\""
	Limits   ResourceValues "json:\"limits,omitzero\""
}

type ResourceValues struct {
	MilliCPU              int64 "json:\"milliCpu,omitempty\""
	MemoryBytes           int64 "json:\"memoryBytes,omitempty\""
	TemporaryStorageBytes int64 "json:\"temporaryStorageBytes,omitempty\""
}

type Scaling struct {
	MinReplicas          int "json:\"minReplicas\""
	MaxReplicas          int "json:\"maxReplicas\""
	CPUTargetUtilization int "json:\"cpuTargetUtilization,omitempty\""
}

type Placement struct {
	SpreadAcross string "json:\"spreadAcross,omitempty\""
	Minimum      int    "json:\"minimum,omitempty\""
	Enforcement  string "json:\"enforcement,omitempty\""
}

type Deployment struct {
	ReleaseCommand ReleaseCommand "json:\"releaseCommand,omitzero\""
	Rollout        Rollout        "json:\"rollout,omitzero\""
}

type ReleaseCommand struct {
	Command       []Expression "json:\"command,omitempty\""
	TimeoutMillis int64        "json:\"timeoutMillis,omitempty\""
}

type Rollout struct {
	Strategy       string "json:\"strategy,omitempty\""
	MaxUnavailable int    "json:\"maxUnavailable,omitempty\""
	MaxSurge       int    "json:\"maxSurge,omitempty\""
	TimeoutMillis  int64  "json:\"timeoutMillis,omitempty\""
}

type Shutdown struct {
	GracePeriodMillis int64 "json:\"gracePeriodMillis,omitempty\""
}

type Volume struct {
	MountPath Expression "json:\"mountPath\""
	SizeBytes int64      "json:\"sizeBytes\""
}

// DatabaseClaim is the lower-level capability requested by a project database
// service. System components can construct the same type without a manifest.
type DatabaseClaim struct {
	Engine                 string   "json:\"engine\""
	Version                string   "json:\"version\""
	Isolation              string   "json:\"isolation\""
	Availability           string   "json:\"availability\""
	StorageBytes           int64    "json:\"storageBytes,omitempty\""
	Extensions             []string "json:\"extensions,omitempty\""
	PointInTimeRecoverySec int64    "json:\"pointInTimeRecoverySeconds,omitempty\""
}

// BucketClaim is the logical object-storage capability requested by a project.
// A later object-store driver allocates it on the physical storage subsystem.
type BucketClaim struct {
	Visibility                         string "json:\"visibility\""
	StorageQuotaBytes                  int64  "json:\"storageQuotaBytes,omitempty\""
	ObjectQuota                        int    "json:\"objectQuota,omitempty\""
	MaxObjectSizeBytes                 int64  "json:\"maxObjectSizeBytes,omitempty\""
	Versioning                         string "json:\"versioning\""
	AbortIncompleteUploadsAfterSeconds int64  "json:\"abortIncompleteUploadsAfterSeconds,omitempty\""
	ExpireNoncurrentVersionsAfterSec   int64  "json:\"expireNoncurrentVersionsAfterSeconds,omitempty\""
}

type Backup struct {
	Schedule         string    "json:\"schedule\""
	RetentionSeconds int64     "json:\"retentionSeconds\""
	Include          Selection "json:\"include\""
}

type Selection struct {
	AllDatabases bool     "json:\"allDatabases,omitempty\""
	Databases    []string "json:\"databases,omitempty\""
	AllBuckets   bool     "json:\"allBuckets,omitempty\""
	Buckets      []string "json:\"buckets,omitempty\""
	AllVolumes   bool     "json:\"allVolumes,omitempty\""
	Volumes      []string "json:\"volumes,omitempty\""
}
