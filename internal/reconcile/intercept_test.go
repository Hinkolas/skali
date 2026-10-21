package reconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/valuestore"
)

// executeDeploymentLocals promotes kernelManifest with an intercept set and
// returns the result plus the definition version for follow-up deploys.
func (f *kernelFixture) executeDeploymentLocals(t *testing.T,
	locals map[string]deploy.LocalApplication) (*deploy.ExecuteResult, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	projects := project.New(f.st)
	draft, err := projects.SubmitDraft(ctx, f.projectID, project.DraftSubmission{
		Source: []byte(kernelManifest), Format: "yaml", ExpectedVersion: 0,
	})
	require.NoError(t, err)
	row, err := f.st.GetDefinitionVersionByHash(ctx, store.GetDefinitionVersionByHashParams{
		ProjectID: f.projectID, DefinitionHash: draft.Hash,
	})
	require.NoError(t, err)
	result := f.executeVersionLocals(t, row.ID, locals)
	return result, row.ID
}

func (f *kernelFixture) executeVersionLocals(t *testing.T, definitionVersion uuid.UUID,
	locals map[string]deploy.LocalApplication) *deploy.ExecuteResult {
	t.Helper()
	ctx := context.Background()
	valueSvc, err := valuestore.New(f.st, strings.Repeat("k", 32))
	require.NoError(t, err)
	candidate, err := valueSvc.Stage(ctx, f.environmentID, map[string]string{
		"APP_DOMAIN":     "demo.example.com",
		"SESSION_SECRET": "kernel-plant-value",
	})
	require.NoError(t, err)
	result, err := f.deploy.Execute(ctx, deploy.ExecuteInput{
		ProjectID:           f.projectID,
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: definitionVersion,
		CandidateID:         candidate.ID,
		Resolver:            &artifactstore.Fake{Store: artifactstore.New(f.st), ProjectID: f.projectID},
		Journal:             f.journal,
		Actor:               "tester",
		LocalApplications:   locals,
	})
	require.NoError(t, err)
	f.revisionID = result.RevisionID
	return result
}

const webInterceptSlice = "intercept-demo-web-affcdc6d146a6bd037773cb30f69840a"

// observeIntercept stands in for the watch delivering what a pass applied
// for the intercepted web application: its selectorless Service and the
// EndpointSlice pointing it at the host.
func (f *kernelFixture) observeIntercept() {
	f.fake.SetService(f.environmentID, f.namespace, f.webServiceName(), "web", nil)
	f.fake.SetEndpointSlice(f.environmentID, f.namespace, webInterceptSlice, "web")
}

// An intercepted application renders Service + EndpointSlice but no
// Deployment, and needs no observed workload: it is healthy, and the
// revision activates, once the routing to the dev process is observed.
// Clearing the intercept re-renders the Deployment and prunes the stale
// slice.
func TestReconcileInterceptedApplication(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()

	result, definitionVersion := f.executeDeploymentLocals(t, map[string]deploy.LocalApplication{
		"web": {Ports: map[string]int32{"http": 5173}},
	})
	f.fake.SetFresh()

	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, requeueHealthCheck, requeue, "the routing is applied but not observed yet")
	require.Nil(t, f.target(t).ActiveRevisionID)

	ops := f.cluster.recorded()
	require.Contains(t, ops, "apply Service/"+f.namespace+"/"+f.webServiceName())
	require.Contains(t, ops, "apply EndpointSlice/"+f.namespace+"/"+webInterceptSlice)
	require.NotContains(t, ops, "apply Deployment/"+f.namespace+"/"+f.webDeploymentName(t))

	status, err := f.kernel.Status(ctx, f.environmentID)
	require.NoError(t, err)
	require.Len(t, status.Services, 1)
	require.True(t, status.Services[0].Intercepted)
	require.Equal(t, module.HealthProgressing, status.Services[0].Health)
	require.Equal(t, "intercept-routing-missing", status.Services[0].Diagnostics[0].Code)
	require.Contains(t, status.Services[0].Diagnostics[0].Message, "Service "+f.webServiceName())
	require.Contains(t, status.Services[0].Diagnostics[0].Message, "EndpointSlice "+webInterceptSlice)

	f.observeIntercept()
	requeue, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Zero(t, requeue, "an intercepted-only revision activates once its routing is observed")
	target := f.target(t)
	require.NotNil(t, target.ActiveRevisionID)
	require.Equal(t, result.RevisionID, *target.ActiveRevisionID)

	status, err = f.kernel.Status(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, module.HealthHealthy, status.Services[0].Health)
	require.Len(t, status.Services[0].Diagnostics, 1)
	require.Equal(t, "intercepted", status.Services[0].Diagnostics[0].Code)

	// Flip back: a deploy without local applications clears the intercept.
	// The stale slice is in the observed snapshot; the next pass re-applies
	// the Deployment and prunes the slice.
	f.executeVersionLocals(t, definitionVersion, nil)

	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	ops = f.cluster.recorded()
	require.Contains(t, ops, "apply Deployment/"+f.namespace+"/"+f.webDeploymentName(t))
	require.Contains(t, ops, "delete EndpointSlice/"+f.namespace+"/"+webInterceptSlice)

	// With the workload healthy again the flip-back activates.
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	target = f.target(t)
	require.NotNil(t, target.ActiveRevisionID)
	require.Equal(t, *target.TargetRevisionID, *target.ActiveRevisionID)
	require.NotEqual(t, result.RevisionID, *target.ActiveRevisionID)
}
