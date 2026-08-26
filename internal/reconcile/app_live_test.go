package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/module"
)

// serviceStatus reads the current projection of one service key.
func (f *liveFixture) serviceStatus(t *testing.T, key string) *ServiceStatus {
	t.Helper()
	status, err := f.kernel.Status(context.Background(), f.environmentID)
	require.NoError(t, err)
	for index := range status.Services {
		if status.Services[index].Key == key {
			return &status.Services[index]
		}
	}
	return nil
}

// Killing one of three application members drops health
// to 2/3 promptly with the replacement member named, then the deployment
// controller heals it back to 3/3 healthy. Exercises the production
// application module end to end.
func TestLivePodKillDegradedAndHeal(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t, Config{RolloutDeadline: 5 * time.Minute}, nil)
	f.start(t)
	ctx := context.Background()

	result := f.deployManifest(t, liveManifest(f.projectName, 3, false))
	f.waitActive(t, result.RevisionID, 3*time.Minute)
	require.Eventually(t, func() bool {
		service := f.serviceStatus(t, "web")
		return service != nil && service.Health == module.HealthHealthy
	}, time.Minute, 200*time.Millisecond, "three members must report healthy")

	pods, err := f.clientset.CoreV1().Pods(f.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: kubernetes.LabelService + "=web",
	})
	require.NoError(t, err)
	require.Len(t, pods.Items, 3)
	victim := pods.Items[0].Name
	require.NoError(t, f.clientset.CoreV1().Pods(f.namespace).Delete(ctx, victim, metav1.DeleteOptions{}))

	// The watch, not any audit, must surface the degradation promptly; the
	// tight poll catches the short window before the replacement readies.
	var degraded *ServiceStatus
	require.Eventually(t, func() bool {
		service := f.serviceStatus(t, "web")
		if service == nil {
			return false
		}
		if service.Health == module.HealthDegraded || service.Health == module.HealthProgressing {
			degraded = service
			return true
		}
		return false
	}, 15*time.Second, 25*time.Millisecond, "the killed member must be visible promptly")

	// A degraded projection names its counts; when the replacement member
	// was already observed, its not-ready diagnostic rides along.
	if degraded.Health == module.HealthDegraded {
		require.NotEmpty(t, degraded.Diagnostics)
		require.Equal(t, "partial-availability", degraded.Diagnostics[0].Code)
		require.Equal(t, "2/3 members ready", degraded.Diagnostics[0].Message)
	}

	require.Eventually(t, func() bool {
		service := f.serviceStatus(t, "web")
		if service == nil || service.Health != module.HealthHealthy {
			return false
		}
		ready := 0
		for _, pod := range service.Pods {
			if pod.Ready {
				ready++
			}
		}
		return ready == 3
	}, 2*time.Minute, 200*time.Millisecond, "the deployment controller must heal back to 3/3")
}
