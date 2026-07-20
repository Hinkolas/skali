package kubernetes

// The stable label contract that indexes every managed object back to its
// installation, project, environment, service, and revision. Selector labels
// (managed, project, application, app.kubernetes.io/name) are baked into
// immutable Deployment and Service selectors and must never gain
// per-revision or per-environment values; object labels carry the full
// identity and may change between revisions.
const (
	LabelManaged     = "skali.dev/managed"
	LabelProject     = "skali.dev/project"
	LabelApplication = "skali.dev/application"
	LabelEnvironment = "skali.dev/environment"
	LabelService     = "skali.dev/service"
	LabelRevision    = "skali.dev/revision"
)

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
