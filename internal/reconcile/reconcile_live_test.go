package reconcile

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/module/app"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
	"github.com/Hinkolas/skali/internal/valuestore"
)

// liveFixture runs the full kernel loop against the k3d test cluster with a
// real ephemeral database. Each fixture gets a unique project name so
// namespaces never collide between parallel tests.
type liveFixture struct {
	st        *store.Store
	deploy    *deploy.Service
	values    *valuestore.Service
	journal   *journal.Service
	kernel    *Kernel
	observed  *observe.Store
	clientset *kubernetes.Clientset

	projectName   string
	projectID     uuid.UUID
	environmentID uuid.UUID
	namespace     string
	manifest      string
	replicas      int
}

func liveManifest(project string, replicas int, autoscaled bool) string {
	scaling := ""
	if replicas > 1 || autoscaled {
		scaling = "    scaling:\n      replicas:\n        min: " + strconv.Itoa(replicas) + "\n"
		if autoscaled {
			scaling += "        max: " + strconv.Itoa(replicas+3) + "\n" +
				"      autoscaling:\n        cpu:\n          targetUtilization: 70\n"
		}
	}
	return "version: \"1\"\nname: " + project + "\napplications:\n  web:\n" +
		"    image: traefik/whoami:v1.10.2\n" +
		"    ports:\n      http:\n        port: 80\n        protocol: http\n" +
		scaling
}

func newLiveFixture(t *testing.T, cfg Config, config *rest.Config) *liveFixture {
	t.Helper()
	if config == nil {
		config = kubetest.Config(t)
	}
	pool := testdb.New(t)
	ctx := context.Background()

	client, err := kube.NewFromConfig(config)
	require.NoError(t, err)
	clientset := kubetest.Clientset(t)

	st := store.NewStore(pool)
	projects := project.New(st)
	valueSvc, err := valuestore.New(st, strings.Repeat("l", 32))
	require.NoError(t, err)
	artifactSvc := artifactstore.New(st)
	deploySvc := deploy.New(st, valueSvc, artifactSvc, "test")
	journalSvc := journal.NewService(st, "live-boot-"+uuid.NewString()[:8])
	// Live tests run the production application module; apptest keeps
	// serving the pure kernel tests over the observe fake.
	registry := module.NewRegistry()
	require.NoError(t, registry.Register(app.Module{}))

	staleThreshold := cfg.StaleThreshold
	if staleThreshold <= 0 {
		staleThreshold = 30 * time.Second
	}
	observed := observe.NewStore(nil)
	var kernel *Kernel
	source := observe.NewKubeSource(client, observed, observe.SourceOptions{
		Resync:         time.Hour, // watches, not resync, must explain propagation
		StaleThreshold: staleThreshold,
		Enqueue:        func(id uuid.UUID) { kernel.Enqueue(id) },
	})
	kernel = New(Deps{
		Store: st, Deploy: deploySvc, Values: valueSvc, Journal: journalSvc,
		Registry: registry, Observed: observed, Source: source, Cluster: client,
	}, cfg)
	deploySvc.SetEnqueuer(kernel)

	projectName := "live-" + uuid.NewString()[:8]
	proj, err := projects.Create(ctx, projectName, "")
	require.NoError(t, err)
	env, err := projects.CreateEnvironment(ctx, proj.ID, "production")
	require.NoError(t, err)
	namespace := "skali-" + projectName + "-production"
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = clientset.CoreV1().Namespaces().Delete(cleanupCtx, namespace, metav1.DeleteOptions{})
	})

	return &liveFixture{
		st: st, deploy: deploySvc, values: valueSvc, journal: journalSvc, kernel: kernel,
		observed: observed, clientset: clientset,
		projectName: projectName, projectID: proj.ID, environmentID: env.ID,
		namespace: namespace,
	}
}

// start runs the kernel loop; the returned stop is idempotent and also
// registered as cleanup.
func (f *liveFixture) start(t *testing.T) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = f.kernel.Run(ctx)
	}()
	stop = func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Log("kernel did not stop within 10s")
		}
	}
	t.Cleanup(stop)
	require.Eventually(t, f.observed.Ready, 30*time.Second, 100*time.Millisecond,
		"observation cache must synchronize before the kernel acts")
	return stop
}

// deployManifest promotes the manifest; the kernel loop owns the rollout.
func (f *liveFixture) deployManifest(t *testing.T, manifest string) *deploy.ExecuteResult {
	t.Helper()
	ctx := context.Background()
	projects := project.New(f.st)
	draft, err := projects.GetDraft(ctx, f.projectID)
	expected := int64(0)
	if err == nil {
		expected = draft.Version
	}
	submitted, err := projects.SubmitDraft(ctx, f.projectID, project.DraftSubmission{
		Source: []byte(manifest), Format: "yaml", ExpectedVersion: expected,
	})
	require.NoError(t, err)
	row, err := f.st.GetDefinitionVersionByHash(ctx, store.GetDefinitionVersionByHashParams{
		ProjectID: f.projectID, DefinitionHash: submitted.Hash,
	})
	require.NoError(t, err)
	result, err := f.deploy.Execute(ctx, deploy.ExecuteInput{
		ProjectID:           f.projectID,
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: row.ID,
		Resolver:            &artifactstore.Fake{Store: artifactstore.New(f.st), ProjectID: f.projectID},
		Journal:             f.journal,
		Actor:               "live-test",
	})
	require.NoError(t, err)
	return result
}

func (f *liveFixture) waitActive(t *testing.T, revisionID uuid.UUID, timeout time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool {
		target, err := f.st.GetEnvironmentTarget(context.Background(), f.environmentID)
		if err != nil || target.ActiveRevisionID == nil {
			return false
		}
		return *target.ActiveRevisionID == revisionID
	}, timeout, 500*time.Millisecond, "revision must activate once health passes")
}

// Live: a promoted revision is applied, becomes healthy on the real
// cluster, and activates; the single deployment run explains the whole
// rollout, and deleting it changes nothing.
func TestLiveDeployToActive(t *testing.T) {
	t.Parallel()
	f := newLiveFixture(t, Config{RolloutDeadline: 5 * time.Minute}, nil)
	f.start(t)

	result := f.deployManifest(t, liveManifest(f.projectName, 1, false))
	f.waitActive(t, result.RevisionID, 3*time.Minute)

	tree, err := f.journal.RunTree(context.Background(), result.RunID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", tree.Run.Status)

	deployment, err := f.clientset.AppsV1().Deployments(f.namespace).Get(
		context.Background(), f.projectName+"-web", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, int32(1), deployment.Status.ReadyReplicas)

	// The environment status projection agrees without any cluster read.
	status, err := f.kernel.Status(context.Background(), f.environmentID)
	require.NoError(t, err)
	require.NotNil(t, status.Active)
	require.Equal(t, result.RevisionID, status.Active.ID)
	require.Len(t, status.Services, 1)
	require.Equal(t, module.HealthHealthy, status.Services[0].Health)
	require.NotEmpty(t, status.Services[0].Pods)
}
