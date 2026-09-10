package reconcile

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/kube"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/layout"
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
	ensurePriorityClasses(t, clientset)

	st := store.NewStore(pool)
	projects := project.New(st)
	valueSvc, err := valuestore.New(st, strings.Repeat("l", 32))
	require.NoError(t, err)
	artifactSvc := artifactstore.New(st)
	deploySvc := deploy.New(st, valueSvc, artifactSvc, "test")
	journalSvc := journal.NewService(st, "live-boot-"+uuid.NewString()[:8])
	// Live tests run the production application module, same as the
	// kernel tests over the observe fake.
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
	proj, err := projects.Create(ctx, projectName, "", uuid.Nil)
	require.NoError(t, err)
	env, err := projects.CreateEnvironment(ctx, proj.ID, "production", project.EnvironmentOptions{})
	require.NoError(t, err)
	// Environment namespaces are named by environment identity, the same
	// derivation the kernel's namespace render uses.
	namespace := "skali-" + env.ID.String()
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

// webDeployment returns the one live Deployment of the fixture's web
// application. Names are derived by the renderer (and vary per color under
// blue-green), so tests find the workload by its application label.
func (f *liveFixture) webDeployment(t *testing.T) *appsv1.Deployment {
	t.Helper()
	list, err := f.clientset.AppsV1().Deployments(f.namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: rendering.LabelApplication + "=web",
	})
	require.NoError(t, err)
	require.Len(t, list.Items, 1, "exactly one web Deployment expected")
	return &list.Items[0]
}

func (f *liveFixture) waitActive(t *testing.T, revisionID uuid.UUID, timeout time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool {
		target, err := f.st.GetEnvironmentTarget(context.Background(), f.environmentID)
		if err != nil || target.ActiveRevisionID == nil || *target.ActiveRevisionID != revisionID {
			return false
		}
		// Activation writes the pointer before concluding the run; a next
		// deployment needs the running-run slot free, so wait for both.
		_, err = f.st.GetRunningRunByEnvironment(context.Background(), &f.environmentID)
		return errors.Is(err, pgx.ErrNoRows)
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

	deployment := f.webDeployment(t)
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

// ensurePriorityClasses installs the PriorityClasses the platform bundle
// would: every application pod names one, and admission rejects pods whose
// class does not exist, so a bare test cluster needs them before any deploy.
func ensurePriorityClasses(t *testing.T, clientset *kubernetes.Clientset) {
	t.Helper()
	ctx := context.Background()
	for name, value := range map[string]int32{
		layout.PriorityClassCritical: layout.PriorityClassCriticalValue,
		layout.PriorityClassHigh:     layout.PriorityClassHighValue,
		layout.PriorityClassNormal:   layout.PriorityClassNormalValue,
	} {
		_, err := clientset.SchedulingV1().PriorityClasses().Create(ctx, &schedulingv1.PriorityClass{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Value:      value,
		}, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			require.NoError(t, err)
		}
	}
}
