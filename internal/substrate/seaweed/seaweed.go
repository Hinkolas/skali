// Package seaweed pins and speaks to the blessed SeaweedFS system, the
// object-storage engine (REWORK_V2 10.5). Like internal/substrate/cnpg for
// databases, it owns the engine facts (image pin, ports, filer paths), the
// identity generators, pure rendering, and the admin client behind the
// substrate's external ensures. It deliberately imports nothing
// substrate-facing: the HTTP transport (the API server's service proxy) is
// injected by the caller.
package seaweed

// Image is the single SeaweedFS pin. Every rendered component uses exactly
// this image; the measured behaviors below (identity channel, deletion
// ordering, bootstrap-auth merge) were verified on this version and must be
// re-verified on any bump.
const Image = "chrislusf/seaweedfs:4.39"

// StoreName is the fixed component-name prefix in skali-platform; with one
// live store per installation the constant is the whole naming scheme.
const StoreName = "seaweed"

// Component names in skali-platform. The Service names are identical in the
// production and dev shapes (dev's Services select the all-in-one pod), so
// endpoints, output mirrors, and the probe are mode-independent.
const (
	MasterService = "seaweed-master"
	VolumeApp     = "seaweed-volume"
	FilerService  = "seaweed-filer"
	S3Service     = "seaweed-s3"
	// AllInOneApp is the dev shape: one `weed server -filer -s3` process.
	AllInOneApp = "seaweed"
)

// Component ports (SeaweedFS defaults; gRPC = HTTP + 10000).
const (
	MasterPort     = 9333
	MasterGRPCPort = 19333
	VolumePort     = 8080
	VolumeGRPCPort = 18080
	FilerPort      = 8888
	FilerGRPCPort  = 18888
	S3Port         = 8333
)

// Region is the advertised S3 region. SeaweedFS accepts any; a stable
// AWS-shaped default keeps every SDK's validation happy.
const Region = "us-east-1"

// Filer paths, the file half of the admin surface, driven over plain filer
// HTTP. Identities are NOT a file concern: SeaweedFS loads them through its
// credential manager and broadcasts updates to the gateways over gRPC, so
// identity administration goes through `s3.configure` (Client.EnsureIdentity
// / DeleteIdentity via pod exec); raw identity-file writes are silently
// ignored at runtime (measured on the pin).
const (
	// BucketsPrefix is where buckets live: one directory = one bucket = one
	// collection, which is what makes bucket deletion free volume files
	// instantly.
	BucketsPrefix = "/buckets/"
	// FilerConfPath is the per-path configuration document (the replication
	// setting on BucketsPrefix, per-bucket readOnly quota flips); the filers
	// subscribe to changes and hot-reload, no restarts.
	FilerConfPath = "/etc/seaweedfs/filer.conf"
)

// The inert bootstrap identity: with zero identities SeaweedFS serves S3
// anonymously (auth is off, measured on the pin), so an identity must exist
// from process start. The rendered gateway carries it in -s3.config (it
// merges with the credential store's identities at boot), and the substrate
// keeps it in the identity set forever so the list can never go empty. Its
// credentials are deliberately public and grant nothing (actions: []); its
// only job is to force authentication on.
const (
	BootstrapIdentityName = "skali-bootstrap-deny"
	BootstrapAccessKey    = "SKALIBOOTSTRAPDENY00"
	BootstrapSecretKey    = "skali-bootstrap-deny-grants-nothing-000"
)

// SourceName is the provider observation source (REWORK_V2 7.4).
const SourceName = "seaweedfs"

// SharedKey is the observe shared key linking the platform-scoped store
// projection to every environment-owned bucket projection.
const SharedKey = "objectstore/" + StoreName

// ReplicationForNodes derives the default volume replication from the
// object-storage node count (owner decision 2026-07-29): one copy on a
// different server when the fleet can hold one, none on a single node.
// SeaweedFS codes are DataCenter/Rack/Server digits; with no rack topology
// declared every node shares the default rack, so "001" is "another
// server".
func ReplicationForNodes(nodes int) string {
	if nodes >= 2 {
		return "001"
	}
	return "000"
}

// MastersForNodes derives the master quorum size: three when the fleet can
// spread a raft quorum, else one (scale-up is a plain re-apply; raft forms
// and volume ids survive, measured on the pin).
func MastersForNodes(nodes int) int {
	if nodes >= 3 {
		return 3
	}
	return 1
}
