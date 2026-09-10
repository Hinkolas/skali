package kubernetes

// The stable label contract that indexes every managed object back to its
// installation, project, environment, service, and revision. Selector labels
// (managed, project, application, app.kubernetes.io/name) are baked into
// immutable Deployment and Service selectors and must never gain
// per-revision or per-environment values; object labels carry the full
// identity and may change between revisions. LabelColor is the one
// exception by design: blue-green applications run one Deployment per pod
// template hash, and the color narrows each Deployment's selector to its
// own pods while the Service selector names the color that serves.
const (
	LabelManaged         = "skali.dev/managed"
	LabelProject         = "skali.dev/project"
	LabelApplication     = "skali.dev/application"
	LabelEnvironment     = "skali.dev/environment"
	LabelEnvironmentName = "skali.dev/environment-name"
	LabelService         = "skali.dev/service"
	LabelRevision        = "skali.dev/revision"
	// LabelPool marks platform-scoped substrate objects (database pools and
	// their tenants) with the owning pool's name. Pool objects carry no
	// environment identity; tenant objects carry both.
	LabelPool = "skali.dev/pool"
	// LabelClaim marks substrate objects belonging to one claim.
	LabelClaim = "skali.dev/claim"

	// EndpointSliceManagedBy is the kube endpointslice controller's
	// ownership label; skali stamps its own value on intercept slices so the
	// controller leaves them alone. The controller copies every Service
	// label onto the slices it manages, the managed label included, so this
	// value is the only thing telling skali's slices from the controller's.
	LabelEndpointSliceManagedBy = "endpointslice.kubernetes.io/managed-by"
	EndpointSliceManagedBySkali = "skali.dev"
)

// InterceptEndpointSliceSelector selects exactly the EndpointSlices skali
// renders for intercepted applications.
const InterceptEndpointSliceSelector = ManagedSelector + "," + LabelEndpointSliceManagedBy + "=" + EndpointSliceManagedBySkali

// AnnotationRestartedAt marks application pod templates with the target's
// restart stamp (kubectl rollout restart semantics): a forced deployment
// updates the stamp, the template changes, and the workload rolls even when
// the revision is unchanged.
const AnnotationRestartedAt = "skali.dev/restarted-at"

// AnnotationValuesHash stamps application pod templates with the identity
// of the project variables the application references, so changing a
// referenced value rolls exactly the referencing workloads. Secret
// variables contribute their stored version, never plaintext.
const AnnotationValuesHash = "skali.dev/values-hash"

// ManagedSelector is the informer-level list/watch selector: every object
// skalid owns carries it, nothing else does.
const ManagedSelector = LabelManaged + "=true"

// EnvironmentSelector selects every managed object of one environment.
func EnvironmentSelector(environmentID string) string {
	return ManagedSelector + "," + LabelEnvironment + "=" + environmentID
}

// RevisionLabelValue shortens a revision checksum to a label-friendly value.
// Sixteen hex characters keep collisions out of practical reach while staying
// readable in kubectl output.
func RevisionLabelValue(checksum string) string {
	if len(checksum) <= 16 {
		return checksum
	}
	return checksum[:16]
}

// LabelColor names one blue-green color: the pod template hash of a
// Deployment. It is the one selector label with a per-object value, and it
// is stable for that object's whole life: a changed template is a new
// Deployment with a new color, never a changed selector. The Service of a
// blue-green application selects exactly one color; switching it is the
// traffic switch.
const LabelColor = "skali.dev/color"
