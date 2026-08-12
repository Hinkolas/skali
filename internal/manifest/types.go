// Package manifest defines and parses Skali's human-authored project format.
// These wire types intentionally mirror skali.yml. The compiler converts them
// into a separate normalized representation before any backend sees them.
package manifest

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// CurrentVersion is the single version knob for the whole authored and
// stored contract: skali.yaml manifests, compiled definition documents, and
// revision documents (revision.SchemaVersion aliases it) all carry it, and
// reads accept exactly this value. Validate enforces it here, schema.go
// injects it into the runtime JSON Schema, and revision.Decode plus
// compiler.DecodeDefinition enforce it on stored documents. Bumping it must
// update the static const in schemas/skali.schema.json in lockstep.
const CurrentVersion = "1"

// Text accepts a YAML/JSON string or number and preserves its textual form.
// It is useful for author-friendly values such as cpu: 0.2 and version: 17.
type Text string

func (t *Text) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.Tag == "!!null" || node.Tag == "!!bool" {
		return fmt.Errorf("must be a string or number")
	}
	*t = Text(node.Value)
	return nil
}

// Scalar is an application environment value. Environment variables are
// ultimately strings, but YAML booleans and numbers are accepted ergonomically.
type Scalar string

func (s *Scalar) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.Tag == "!!null" {
		return fmt.Errorf("must be a scalar value")
	}
	*s = Scalar(node.Value)
	return nil
}

// Selection is either the keyword "all" or a list of stable resource keys.
type Selection struct {
	All  bool
	Keys []string
}

func (s *Selection) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Value != "all" {
			return fmt.Errorf("must be \"all\" or a list of resource keys")
		}
		s.All = true
		s.Keys = nil
		return nil
	case yaml.SequenceNode:
		var keys []string
		if err := node.Decode(&keys); err != nil {
			return fmt.Errorf("must be \"all\" or a list of resource keys: %w", err)
		}
		s.All = false
		s.Keys = keys
		return nil
	default:
		return fmt.Errorf("must be \"all\" or a list of resource keys")
	}
}

func (s Selection) MarshalJSON() ([]byte, error) {
	if s.All {
		return json.Marshal("all")
	}
	return json.Marshal(s.Keys)
}

type Project struct {
	Version      string                 `yaml:"version" json:"version" jsonschema:"Manifest schema version. Currently 1."`
	Name         string                 `yaml:"name" json:"name" jsonschema:"Stable project name."`
	Description  string                 `yaml:"description,omitempty" json:"description,omitempty" jsonschema:"Human-readable project description."`
	Applications map[string]Application `yaml:"applications,omitempty" json:"applications,omitempty" jsonschema:"Container applications keyed by stable service name."`
	Databases    map[string]Database    `yaml:"databases,omitempty" json:"databases,omitempty" jsonschema:"Managed databases keyed by stable service name."`
	Buckets      map[string]Bucket      `yaml:"buckets,omitempty" json:"buckets,omitempty" jsonschema:"Managed object-storage buckets keyed by stable service name."`
	Backups      map[string]Backup      `yaml:"backups,omitempty" json:"backups,omitempty" jsonschema:"Project-wide coordinated backup policies."`
}

type Application struct {
	Image       string            `yaml:"image,omitempty" json:"image,omitempty" jsonschema:"Existing OCI image reference. Mutually exclusive with build."`
	Build       Build             `yaml:"build,omitempty" json:"build,omitempty" jsonschema:"Project source build. Mutually exclusive with image."`
	Command     []string          `yaml:"command,omitempty" json:"command,omitempty" jsonschema:"Container command and arguments."`
	Environment map[string]Scalar `yaml:"environment,omitempty" json:"environment,omitempty" jsonschema:"Environment variables exposed to the application."`
	Ports       map[string]Port   `yaml:"ports,omitempty" json:"ports,omitempty" jsonschema:"Named application ports."`
	Routes      map[string]Route  `yaml:"routes,omitempty" json:"routes,omitempty" jsonschema:"Public HTTP routes."`
	Health      Health            `yaml:"health,omitempty" json:"health,omitempty" jsonschema:"Startup, readiness, and liveness health checks."`
	Resources   Resources         `yaml:"resources,omitempty" json:"resources,omitempty" jsonschema:"Per-replica resource reservations and limits."`
	Scaling     Scaling           `yaml:"scaling,omitempty" json:"scaling,omitempty" jsonschema:"Replica and autoscaling policy."`
	Placement   Placement         `yaml:"placement,omitempty" json:"placement,omitempty" jsonschema:"Replica placement preferences."`
	Deployment  Deployment        `yaml:"deployment,omitempty" json:"deployment,omitempty" jsonschema:"Release and rollout behavior."`
	Shutdown    Shutdown          `yaml:"shutdown,omitempty" json:"shutdown,omitempty" jsonschema:"Graceful shutdown behavior."`
	Volumes     map[string]Volume `yaml:"volumes,omitempty" json:"volumes,omitempty" jsonschema:"Persistent volumes mounted by the application."`
	// Commands and Dev are client-only authoring surface: they drive host-side
	// behavior (skali dev interception and skali dev run) and are deliberately
	// not mirrored into compiler.Application, the compiled definition, or its
	// hash. Mirroring them would change every environment's plan diff for a
	// purely local concern.
	Commands map[string][]string `yaml:"commands,omitempty" json:"commands,omitempty" jsonschema:"Named host-side commands run with the application's resolved environment via skali dev run."`
	Dev      Dev                 `yaml:"dev,omitempty" json:"dev,omitempty" jsonschema:"Local dev server mode: skali dev runs this command on the host instead of building the application."`
}

// Dev describes an application's local dev server. When present, skali dev
// skips building the application and runs the command on the host with the
// application's resolved environment, while cluster routes are intercepted to
// reach the host process. Every service port without a pin gets a
// deterministic auto-allocated host port; the chosen ports are injected into
// the command's environment as SKALI_PORT_<NAME> (plus PORT for a single
// port) and are available as ${VAR} in the command elements.
type Dev struct {
	Command []string       `yaml:"command,omitempty" json:"command,omitempty" jsonschema:"Host command that starts the dev server; ${VAR} expands from the command's environment, including the injected port variables."`
	Ports   map[string]int `yaml:"ports,omitempty" json:"ports,omitempty" jsonschema:"Optional pins: application port name mapped to a fixed host port. Unmapped service ports are auto-allocated and injected as SKALI_PORT_<NAME> (plus PORT when the application has a single port)."`
}

type Build struct {
	Context    string            `yaml:"context,omitempty" json:"context,omitempty" jsonschema:"Build context relative to the project root."`
	Dockerfile string            `yaml:"dockerfile,omitempty" json:"dockerfile,omitempty" jsonschema:"Dockerfile path relative to the build context."`
	Target     string            `yaml:"target,omitempty" json:"target,omitempty" jsonschema:"Optional multi-stage build target."`
	Arguments  map[string]Scalar `yaml:"arguments,omitempty" json:"arguments,omitempty" jsonschema:"Literal build arguments. They persist in the image configuration, so never put credentials here."`
}

type Port struct {
	Port     int    `yaml:"port" json:"port" jsonschema:"Container port number."`
	Protocol string `yaml:"protocol,omitempty" json:"protocol,omitempty" jsonschema:"Application protocol: http, https, tcp, or udp."`
}

type Route struct {
	Domain string `yaml:"domain" json:"domain" jsonschema:"Public hostname. Project variable expressions are allowed."`
	Path   string `yaml:"path,omitempty" json:"path,omitempty" jsonschema:"Segment-aware path prefix. Defaults to /."`
	Port   Text   `yaml:"port" json:"port" jsonschema:"Named application port or numeric target port."`
	TLS    string `yaml:"tls,omitempty" json:"tls,omitempty" jsonschema:"TLS policy: automatic or disabled."`
}

type Health struct {
	Startup   Probe `yaml:"startup,omitempty" json:"startup,omitempty"`
	Readiness Probe `yaml:"readiness,omitempty" json:"readiness,omitempty"`
	Liveness  Probe `yaml:"liveness,omitempty" json:"liveness,omitempty"`
}

type Probe struct {
	HTTP             HTTPProbe `yaml:"http,omitempty" json:"http,omitempty"`
	Interval         Text      `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timeout          Text      `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	FailureThreshold int       `yaml:"failureThreshold,omitempty" json:"failureThreshold,omitempty"`
}

type HTTPProbe struct {
	Port Text   `yaml:"port,omitempty" json:"port,omitempty"`
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
}

type Resources struct {
	Requests ResourceValues `yaml:"requests,omitempty" json:"requests,omitempty"`
	Limits   ResourceValues `yaml:"limits,omitempty" json:"limits,omitempty"`
}

type ResourceValues struct {
	CPU              Text `yaml:"cpu,omitempty" json:"cpu,omitempty" jsonschema:"CPU cores, for example 0.2 or 1."`
	Memory           Text `yaml:"memory,omitempty" json:"memory,omitempty" jsonschema:"Memory using Skali units such as MB, GB, MiB, or GiB."`
	TemporaryStorage Text `yaml:"temporaryStorage,omitempty" json:"temporaryStorage,omitempty" jsonschema:"Temporary storage using Skali byte units."`
}

type Scaling struct {
	Replicas    Replicas    `yaml:"replicas,omitempty" json:"replicas,omitempty"`
	Autoscaling Autoscaling `yaml:"autoscaling,omitempty" json:"autoscaling,omitempty"`
}

type Replicas struct {
	Min int `yaml:"min,omitempty" json:"min,omitempty"`
	Max int `yaml:"max,omitempty" json:"max,omitempty"`
}

type Autoscaling struct {
	CPU CPUAutoscaling `yaml:"cpu,omitempty" json:"cpu,omitempty"`
}

type CPUAutoscaling struct {
	TargetUtilization int `yaml:"targetUtilization,omitempty" json:"targetUtilization,omitempty"`
}

type Placement struct {
	Spread Spread `yaml:"spread,omitempty" json:"spread,omitempty"`
}

type Spread struct {
	Across      string `yaml:"across,omitempty" json:"across,omitempty"`
	Minimum     int    `yaml:"minimum,omitempty" json:"minimum,omitempty"`
	Enforcement string `yaml:"enforcement,omitempty" json:"enforcement,omitempty"`
}

type Deployment struct {
	ReleaseCommand ReleaseCommand `yaml:"releaseCommand,omitempty" json:"releaseCommand,omitempty"`
	Rollout        Rollout        `yaml:"rollout,omitempty" json:"rollout,omitempty"`
}

type ReleaseCommand struct {
	Command []string `yaml:"command,omitempty" json:"command,omitempty"`
	Timeout Text     `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

type Rollout struct {
	Strategy       string `yaml:"strategy,omitempty" json:"strategy,omitempty"`
	MaxUnavailable int    `yaml:"maxUnavailable,omitempty" json:"maxUnavailable,omitempty"`
	MaxSurge       int    `yaml:"maxSurge,omitempty" json:"maxSurge,omitempty"`
	Timeout        Text   `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

type Shutdown struct {
	GracePeriod Text `yaml:"gracePeriod,omitempty" json:"gracePeriod,omitempty"`
}

type Volume struct {
	MountPath string `yaml:"mountPath" json:"mountPath"`
	Size      Text   `yaml:"size" json:"size"`
}

type Database struct {
	Engine       string   `yaml:"engine" json:"engine"`
	Version      Text     `yaml:"version" json:"version"`
	Isolation    string   `yaml:"isolation,omitempty" json:"isolation,omitempty"`
	Availability string   `yaml:"availability,omitempty" json:"availability,omitempty"`
	Storage      Storage  `yaml:"storage,omitempty" json:"storage,omitempty"`
	Extensions   []string `yaml:"extensions,omitempty" json:"extensions,omitempty"`
	Recovery     Recovery `yaml:"recovery,omitempty" json:"recovery,omitempty"`
}

type Storage struct {
	Size Text `yaml:"size,omitempty" json:"size,omitempty"`
}

type Recovery struct {
	PointInTime Text `yaml:"pointInTime,omitempty" json:"pointInTime,omitempty"`
}

type Bucket struct {
	Visibility string          `yaml:"visibility,omitempty" json:"visibility,omitempty"`
	Quotas     BucketQuotas    `yaml:"quotas,omitempty" json:"quotas,omitempty"`
	Versioning string          `yaml:"versioning,omitempty" json:"versioning,omitempty"`
	Lifecycle  BucketLifecycle `yaml:"lifecycle,omitempty" json:"lifecycle,omitempty"`
}

type BucketQuotas struct {
	Storage       Text `yaml:"storage,omitempty" json:"storage,omitempty"`
	Objects       int  `yaml:"objects,omitempty" json:"objects,omitempty"`
	MaxObjectSize Text `yaml:"maxObjectSize,omitempty" json:"maxObjectSize,omitempty"`
}

type BucketLifecycle struct {
	AbortIncompleteUploadsAfter   Text `yaml:"abortIncompleteUploadsAfter,omitempty" json:"abortIncompleteUploadsAfter,omitempty"`
	ExpireNoncurrentVersionsAfter Text `yaml:"expireNoncurrentVersionsAfter,omitempty" json:"expireNoncurrentVersionsAfter,omitempty"`
}

type Backup struct {
	Schedule  string        `yaml:"schedule" json:"schedule"`
	Retention Text          `yaml:"retention" json:"retention"`
	Include   BackupInclude `yaml:"include" json:"include"`
}

type BackupInclude struct {
	Databases Selection `yaml:"databases,omitempty" json:"databases,omitempty"`
	Buckets   Selection `yaml:"buckets,omitempty" json:"buckets,omitempty"`
	Volumes   Selection `yaml:"volumes,omitempty" json:"volumes,omitempty"`
}
