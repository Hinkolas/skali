package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/edge/edgeprobe"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/module"
)

// aliasManifest is certManifest with one alias on the public route.
const aliasManifest = `name: demo
applications:
  web:
    image: ghcr.io/example/web:1.0.0
    ports:
      http:
        port: 8080
        protocol: http
    routes:
      public:
        domain: "${APP_DOMAIN}"
        port: http
        aliases:
          - "www.${APP_DOMAIN}"
    environment:
      APP_DOMAIN: "${APP_DOMAIN}"
      SESSION_SECRET: "${SESSION_SECRET}"
`

// aliasCertName is the alias's Certificate name, composed from the
// compiled manifest exactly as the renderer composes it.
func aliasCertName(t *testing.T) string {
	t.Helper()
	document, err := manifest.Parse([]byte(aliasManifest), "skali.yml")
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)
	return rendering.AliasTLSName("demo", "web", "public", result.Definition.Applications["web"].Routes["public"].Aliases[0])
}

// probeByDomain answers reachable for the route domain and unreachable
// (DNS still elsewhere) for every other hostname.
func probeByDomain(reachable string) func(context.Context, string) edgeprobe.Result {
	return func(_ context.Context, domain string) edgeprobe.Result {
		if domain == reachable {
			return edgeprobe.Result{Domain: domain, State: edgeprobe.StateReachable, Addresses: []edgeprobe.AddressResult{oursAddress},
				Message: "stub verdict: reachable", CheckedAt: time.Now()}
		}
		return edgeprobe.Result{Domain: domain, State: edgeprobe.StateUnreachable, Addresses: []edgeprobe.AddressResult{foreignAddress},
			Message: "stub verdict: unreachable", CheckedAt: time.Now()}
	}
}

// An alias whose DNS still points elsewhere is deferred on its own
// checkpoint: the route's certificate issues, the rollout activates, the
// alias checkpoint ends skipped naming the alias, and status, the probe
// list, and the certificate gate all carry the alias next to its route.
func TestRouteAliasDeferredOnItsOwn(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	aliasCert := aliasCertName(t)
	f.kernel.deps.ProbeDomain = probeByDomain("demo.example.com")

	result := f.executeDeploymentManifest(t, aliasManifest)
	f.fake.SetFresh()
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	recorded := f.cluster.recorded()
	require.Contains(t, recorded, "apply Certificate/"+f.namespace+"/"+certName)
	require.Contains(t, recorded, "apply Certificate/"+f.namespace+"/"+aliasCert)

	f.markHealthy(t)
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web",
		module.CertificateStatus{Ready: true, NotAfter: time.Now().Add(60 * 24 * time.Hour)})
	f.fake.SetCertificate(f.environmentID, f.namespace, aliasCert, "web",
		module.CertificateStatus{Issuing: true, Reason: "Pending", Message: "waiting for the ACME challenge"})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)

	require.NotNil(t, f.target(t).ActiveRevisionID, "a deferred alias must not hold activation")
	run, err := f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", run.Status)

	routeStep, found, err := f.kernel.deps.Journal.FindStep(ctx, result.RunID, "tls:"+certName)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "succeeded", routeStep.Status)
	require.Equal(t, "Issue TLS certificate for web / public", routeStep.Title)
	aliasStep, found, err := f.kernel.deps.Journal.FindStep(ctx, result.RunID, "tls:"+aliasCert)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "skipped", aliasStep.Status)
	require.Equal(t, "Issue TLS certificate for web / public alias www.demo.example.com", aliasStep.Title)
	message, err := f.kernel.deps.Journal.LatestStepMessage(ctx, aliasStep.ID)
	require.NoError(t, err)
	require.Contains(t, message, "TLS deferred · www.demo.example.com does not reach this installation yet")

	status, err := f.kernel.Status(ctx, f.environmentID)
	require.NoError(t, err)
	web := status.Services[0]
	require.Equal(t, module.HealthHealthy, web.Health)
	require.Len(t, web.Routes, 1)
	route := web.Routes[0]
	require.Equal(t, "demo.example.com", route.Domain)
	require.False(t, route.Edge.Deferred)
	require.Len(t, route.Aliases, 1)
	alias := route.Aliases[0]
	require.Equal(t, "www.demo.example.com", alias.Domain)
	require.NotNil(t, alias.Certificate)
	require.Equal(t, "issuing", alias.Certificate.State)
	require.Equal(t, aliasCert, alias.Certificate.Name)
	require.NotNil(t, alias.Edge)
	require.True(t, alias.Edge.Deferred)
	var deferred []string
	for _, diagnostic := range web.Diagnostics {
		if diagnostic.Code == "certificate-deferred" {
			deferred = append(deferred, diagnostic.Message)
		}
	}
	require.Len(t, deferred, 1)
	require.Contains(t, deferred[0], "www.demo.example.com does not reach this installation yet")

	probes, err := f.kernel.ProbeRoutes(ctx, f.environmentID)
	require.NoError(t, err)
	require.Len(t, probes, 2)
	require.Equal(t, "demo.example.com", probes[0].Domain)
	require.False(t, probes[0].Alias)
	require.Equal(t, "www.demo.example.com", probes[1].Domain)
	require.True(t, probes[1].Alias)
	require.Equal(t, "public", probes[1].Key)
}

// An alias that does reach this installation gates like a route: its
// hostname is just as public, so a pending issuance holds activation.
func TestRouteAliasReachableGates(t *testing.T) {
	t.Parallel()
	f := certFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	aliasCert := aliasCertName(t)
	f.kernel.deps.ProbeDomain = probeResult(nil, edgeprobe.StateReachable, oursAddress)

	f.executeDeploymentManifest(t, aliasManifest)
	f.fake.SetFresh()
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.markHealthy(t)
	f.fake.SetCertificate(f.environmentID, f.namespace, certName, "web",
		module.CertificateStatus{Ready: true, NotAfter: time.Now().Add(60 * 24 * time.Hour)})
	f.fake.SetCertificate(f.environmentID, f.namespace, aliasCert, "web",
		module.CertificateStatus{Issuing: true, Reason: "Pending", Message: "waiting for the ACME challenge"})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Nil(t, f.target(t).ActiveRevisionID, "a pending alias certificate holds activation")

	f.fake.SetCertificate(f.environmentID, f.namespace, aliasCert, "web",
		module.CertificateStatus{Ready: true, NotAfter: time.Now().Add(60 * 24 * time.Hour)})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.NotNil(t, f.target(t).ActiveRevisionID)
}
