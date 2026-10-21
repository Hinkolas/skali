package reconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/edge"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
)

// interceptDatabaseManifest is certManifest's routed web application
// reading a database with two extensions: the shape of the #233 project.
var interceptDatabaseManifest = strings.Replace(certManifest, "      SESSION_SECRET:",
	"      DATABASE_URL: \"{{ databases.data.url }}\"\n      SESSION_SECRET:", 1) +
	"databases:\n  data:\n    engine: postgres\n    version: 17\n    extensions: [pg_trgm, unaccent]\n"

const (
	webPrimaryRoute = "route-demo-web-public-primary-84d18c2ac3f6fedc1433f38e899e23bf"
	webHTTPRoute    = "route-demo-web-public-http-5f1b04c211d4f06c08a014badc51a3fb"
)

var interceptLocals = map[string]deploy.LocalApplication{"web": {Ports: map[string]int32{"http": 5173}}}

// deployInterceptDatabase promotes interceptDatabaseManifest with web served
// by a local dev process and the claim manager scripted by claims, on a
// fresh observation with web's certificate issued.
func (f *kernelFixture) deployInterceptDatabase(t *testing.T, claims *fakeClaims) (*deploy.ExecuteResult, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	draft, err := project.New(f.st).SubmitDraft(ctx, f.projectID, project.DraftSubmission{
		Source: []byte(interceptDatabaseManifest), Format: "yaml",
	})
	require.NoError(t, err)
	row, err := f.st.GetDefinitionVersionByHash(ctx, store.GetDefinitionVersionByHashParams{
		ProjectID: f.projectID, DefinitionHash: draft.Hash,
	})
	require.NoError(t, err)
	f.kernel.deps.Claims = claims
	f.provisionData(claims)
	f.fake.SetFresh()
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web", module.CertificateStatus{
		Ready: true, NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"demo.example.com"},
	})
	return f.executeVersionLocals(t, row.ID, interceptLocals), row.ID
}

// observeInterceptRoutes stands in for the watch delivering all of web's
// routing: its Service and EndpointSlice and both IngressRoutes of its
// route. It returns the refs of the objects a pause deletes.
func (f *kernelFixture) observeInterceptRoutes() []kube.ObjectRef {
	f.fake.SetEndpointSlice(f.environmentID, f.namespace, webInterceptSlice, "web")
	return []kube.ObjectRef{
		f.seedObject(schema.GroupVersionKind{Version: "v1", Kind: "Service"}, module.KindService, f.namespace, f.webServiceName(), "web"),
		f.seedObject(edge.IngressRouteGVK, module.KindIngress, f.namespace, webPrimaryRoute, "web"),
		f.seedObject(edge.IngressRouteGVK, module.KindIngress, f.namespace, webHTTPRoute, "web"),
	}
}

func (f *kernelFixture) serviceHealth(t *testing.T) map[string]ServiceStatus {
	t.Helper()
	status, err := f.kernel.Status(context.Background(), f.environmentID)
	require.NoError(t, err)
	services := make(map[string]ServiceStatus, len(status.Services))
	for _, service := range status.Services {
		services[service.Type+"."+service.Key] = service
	}
	return services
}

// A claim that is not ready withholds its dependents' apply, so nothing the
// cluster shows for them belongs to the target revision. With the database
// provisioned but its extensions pending, and web's routing and certificate
// left by an earlier session, every service evaluates healthy; the rollout
// must still wait instead of activating a revision whose Service and routes
// it never applied (#233).
func TestWithheldServicesBlockActivation(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	claims := &fakeClaims{}
	result, _ := f.deployInterceptDatabase(t, claims)
	claims.states = []ClaimState{{Service: "databases.data", Waiting: "applying the requested extensions"}}
	f.observeInterceptRoutes()

	for range 2 {
		_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
		require.NoError(t, err)
	}
	require.NotContains(t, f.cluster.recorded(), "apply Service/"+f.namespace+"/"+f.webServiceName())
	for key, service := range f.serviceHealth(t) {
		require.Equal(t, module.HealthHealthy, service.Health, key)
	}
	require.Nil(t, f.target(t).ActiveRevisionID)
	tree, err := f.journal.RunTree(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "running", tree.Run.Status)
	steps := flattenSteps(tree.Steps)
	require.Equal(t, "waiting", steps["claim:databases.data"])
	require.Equal(t, "waiting", steps["apply:web"])

	// The extensions apply: web's routing is applied and the revision
	// activates.
	claims.states = []ClaimState{{Service: "databases.data", Provisioned: true}}
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	ops := f.cluster.recorded()
	require.Contains(t, ops, "apply Service/"+f.namespace+"/"+f.webServiceName())
	require.Contains(t, ops, "apply IngressRoute/"+f.namespace+"/"+webPrimaryRoute)
	require.NotNil(t, f.target(t).ActiveRevisionID)
	tree, err = f.journal.RunTree(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", tree.Run.Status)
}

// Past the deadline the failure names the withheld services: their health
// alone reads fine, so "rollout deadline exceeded" would explain nothing.
func TestRolloutDeadlineNamesWithheldServices(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Nanosecond})
	ctx := context.Background()
	claims := &fakeClaims{}
	result, _ := f.deployInterceptDatabase(t, claims)
	claims.states = []ClaimState{{Service: "databases.data", Waiting: "applying the requested extensions"}}
	f.observeInterceptRoutes()

	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	run, err := f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "failed", run.Status)
	require.NotNil(t, run.Failure)
	require.Contains(t, *run.Failure, "databases.data: applying the requested extensions")
	require.Contains(t, *run.Failure, "applications.web: waiting for databases.data")
}

// Ctrl-C in skali dev pauses the project: teardown deletes web's Service and
// IngressRoutes but keeps its certificate and intercept EndpointSlice. The
// next session applies the routing again, and until the watch shows it,
// status reports what is missing instead of a healthy web (#233).
func TestInterceptPauseResumeRestoresRouting(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	claims := &fakeClaims{}
	_, definitionVersion := f.deployInterceptDatabase(t, claims)
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	removed := f.observeInterceptRoutes()
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.NotNil(t, f.target(t).ActiveRevisionID)

	// Pause, the server operation the CLI invokes on Ctrl-C, and deliver
	// the watch deletes.
	f.seedObject(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}, "namespace", "", f.namespace, "")
	_, _, err = f.deploy.Teardown(ctx, f.environmentID, false, f.journal, "tester")
	require.NoError(t, err)
	f.cluster.ops = nil
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	for _, ref := range removed {
		require.Contains(t, f.cluster.recorded(), "delete "+ref.String())
		f.fake.Remove(ref)
	}
	for _, op := range f.cluster.recorded() {
		require.NotContains(t, op, "Certificate/")
		require.NotContains(t, op, "EndpointSlice/")
	}
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)

	// Resume the same revision: the routing applies again, and web is not
	// healthy (nor the revision active) before the watch shows it.
	result := f.executeVersionLocals(t, definitionVersion, interceptLocals)
	f.cluster.ops = nil
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	ops := f.cluster.recorded()
	require.Contains(t, ops, "apply Service/"+f.namespace+"/"+f.webServiceName())
	require.Contains(t, ops, "apply IngressRoute/"+f.namespace+"/"+webPrimaryRoute)
	require.Contains(t, ops, "apply IngressRoute/"+f.namespace+"/"+webHTTPRoute)

	web := f.serviceHealth(t)["application.web"]
	require.Equal(t, module.HealthProgressing, web.Health)
	require.Equal(t, "intercept-routing-missing", web.Diagnostics[0].Code)
	require.Contains(t, web.Diagnostics[0].Message, "Service "+f.webServiceName())
	require.Contains(t, web.Diagnostics[0].Message, "IngressRoute "+webPrimaryRoute)
	require.NotContains(t, web.Diagnostics[0].Message, "EndpointSlice")
	tree, err := f.journal.RunTree(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "running", tree.Run.Status)

	f.observeInterceptRoutes()
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, module.HealthHealthy, f.serviceHealth(t)["application.web"].Health)
	tree, err = f.journal.RunTree(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", tree.Run.Status)
}
