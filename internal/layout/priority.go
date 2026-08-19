package layout

// PriorityClasses the system bundle installs. Kubernetes resolves them at
// pod admission: the scheduler preempts lower classes when a higher one
// cannot be placed, and the kubelet evicts lower classes first under node
// pressure. Skali's own pods outrank every application; applications of
// high priority environments outrank normal ones; normal carries the rank
// of an unclassed pod, so introducing it changes nothing for existing
// workloads. The values are immutable once the classes exist (a change
// means delete and recreate during converge), so they are final.
const (
	// PriorityClassCritical is skali itself: skalid, the registry, the
	// console, object storage, and the managed databases.
	PriorityClassCritical = "skali-critical"
	// PriorityClassHigh is applications of environments with priority high.
	PriorityClassHigh = "skali-high"
	// PriorityClassNormal is applications of environments with priority
	// normal.
	PriorityClassNormal = "skali-normal"

	PriorityClassCriticalValue = 100000000
	PriorityClassHighValue     = 1000000
	PriorityClassNormalValue   = 0
)

// PriorityClassFor maps an environment's priority setting to the class its
// application pods carry; anything but high is normal.
func PriorityClassFor(priority string) string {
	if priority == "high" {
		return PriorityClassHigh
	}
	return PriorityClassNormal
}
