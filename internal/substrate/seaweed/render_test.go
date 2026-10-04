package seaweed

import (
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestRenderProductionShape(t *testing.T) {
	t.Parallel()
	spec := StoreSpec{Namespace: "skali-platform", Masters: 3, Filers: 2, Replication: "001", Managed: true}
	objects := RenderProduction(spec)

	var sts *appsv1.StatefulSet
	var daemon *appsv1.DaemonSet
	var filer *appsv1.Deployment
	services := map[string]*corev1.Service{}
	for _, object := range objects {
		switch typed := object.(type) {
		case *appsv1.StatefulSet:
			sts = typed
		case *appsv1.DaemonSet:
			daemon = typed
		case *appsv1.Deployment:
			filer = typed
		case *corev1.Service:
			services[typed.Name] = typed
		}
	}

	require.NotNil(t, sts)
	require.EqualValues(t, 3, *sts.Spec.Replicas)
	require.Contains(t, sts.Spec.Template.Spec.Containers[0].Args, "-defaultReplication=001")
	require.Contains(t, sts.Spec.Template.Spec.Containers[0].Args,
		"-peers=seaweed-master-0.seaweed-master.skali-platform.svc.cluster.local:9333,"+
			"seaweed-master-1.seaweed-master.skali-platform.svc.cluster.local:9333,"+
			"seaweed-master-2.seaweed-master.skali-platform.svc.cluster.local:9333")
	require.Equal(t, "true", sts.Spec.Template.Spec.NodeSelector["skali.dev/capability-object-storage"])
	require.Equal(t, "skali-critical", sts.Spec.Template.Spec.PriorityClassName)

	require.NotNil(t, daemon)
	require.Equal(t, "skali-critical", daemon.Spec.Template.Spec.PriorityClassName)
	require.Equal(t, VolumeHostPath, daemon.Spec.Template.Spec.Volumes[0].HostPath.Path)
	require.Equal(t, "true", daemon.Spec.Template.Spec.NodeSelector["skali.dev/capability-object-storage"])

	require.NotNil(t, filer)
	require.EqualValues(t, 2, *filer.Spec.Replicas)
	require.Equal(t, "true", filer.Spec.Template.Spec.NodeSelector["skali.dev/capability-object-storage"],
		"filers sit with the store, not on whichever node has room")
	require.Contains(t, filer.Spec.Template.Spec.Containers[0].Args, "-s3")
	require.Equal(t, FilerStoreSecret, filer.Spec.Template.Spec.Containers[0].EnvFrom[0].SecretRef.Name)

	// The managed label never appears: platform components must not enter
	// environment pruning or the managed informers.
	for _, object := range objects {
		switch typed := object.(type) {
		case *appsv1.Deployment:
			require.NotContains(t, typed.Labels, "skali.dev/managed")
			require.NotContains(t, typed.Spec.Template.Labels, "skali.dev/managed")
		case *appsv1.StatefulSet:
			require.NotContains(t, typed.Labels, "skali.dev/managed")
		case *appsv1.DaemonSet:
			require.NotContains(t, typed.Labels, "skali.dev/managed")
		}
	}
	require.Equal(t, map[string]string{"app": FilerService}, services[S3Service].Spec.Selector)
}

func TestRenderDevServicesSelectAllInOne(t *testing.T) {
	t.Parallel()
	spec := StoreSpec{Namespace: "skali-platform", Masters: 1, Replication: "000"}
	names := map[string]bool{}
	for _, object := range RenderDev(spec) {
		if service, ok := object.(*corev1.Service); ok {
			names[service.Name] = true
			require.Equal(t, map[string]string{"app": AllInOneApp}, service.Spec.Selector,
				"dev services select the all-in-one pod under the same names")
		}
	}
	require.True(t, names[MasterService] && names[FilerService] && names[S3Service])
}

func TestTopologyDerivation(t *testing.T) {
	t.Parallel()
	require.Equal(t, 1, MastersForNodes(1))
	require.Equal(t, 1, MastersForNodes(2))
	require.Equal(t, 3, MastersForNodes(3))
	require.Equal(t, 3, MastersForNodes(5))
	require.Equal(t, 1, FilersForNodes(0))
	require.Equal(t, 1, FilersForNodes(1))
	require.Equal(t, 2, FilersForNodes(2))
	require.Equal(t, 2, FilersForNodes(5))
	require.Equal(t, "000", ReplicationForNodes(1))
	require.Equal(t, "001", ReplicationForNodes(2))
	require.Equal(t, "001", ReplicationForNodes(4))
}

// The filer store Secret reaches the process as env read at start; the
// spec's config hash on the pod template is what rolls the filers when
// the store config changes, in both shapes.
func TestRenderStoreConfigHashRollsFilers(t *testing.T) {
	t.Parallel()
	find := func(objects []runtime.Object, name string) *appsv1.Deployment {
		for _, obj := range objects {
			if deployment, ok := obj.(*appsv1.Deployment); ok && deployment.Name == name {
				return deployment
			}
		}
		return nil
	}
	spec := StoreSpec{Namespace: "skali-platform", Masters: 1, Replication: "000", Managed: true, StoreConfigHash: "abc123"}
	filer := find(RenderProduction(spec), FilerService)
	require.NotNil(t, filer)
	require.Equal(t, "abc123", filer.Spec.Template.Annotations[AnnotationConfigHash])
	spec.Managed = false
	allInOne := find(RenderDev(spec), AllInOneApp)
	require.NotNil(t, allInOne)
	require.Equal(t, "abc123", allInOne.Spec.Template.Annotations[AnnotationConfigHash])
	spec.StoreConfigHash = ""
	require.Nil(t, find(RenderDev(spec), AllInOneApp).Spec.Template.Annotations, "no hash, no annotation")
}

// TestRenderProductionOperability: every component declares resources and
// liveness/startup probes, the filer rolls without a gap and is ready only
// when its S3 gateway answers, disruption budgets guard the quorum and the
// gateway, and the master maintenance loop actually applies replica
// repair (without -apply the pin only reports).
func TestRenderProductionOperability(t *testing.T) {
	t.Parallel()
	three := RenderProduction(StoreSpec{Namespace: "skali-platform", Masters: 3, Filers: 2, Replication: "001", Managed: true})
	var sts *appsv1.StatefulSet
	var daemon *appsv1.DaemonSet
	var filer *appsv1.Deployment
	var config *corev1.ConfigMap
	budgets := map[string]*policyv1.PodDisruptionBudget{}
	for _, object := range three {
		switch typed := object.(type) {
		case *appsv1.StatefulSet:
			sts = typed
		case *appsv1.DaemonSet:
			daemon = typed
		case *appsv1.Deployment:
			filer = typed
		case *policyv1.PodDisruptionBudget:
			budgets[typed.Name] = typed
		case *corev1.ConfigMap:
			if typed.Name == "seaweed-master-config" {
				config = typed
			}
		}
	}
	for name, container := range map[string]corev1.Container{
		"master": sts.Spec.Template.Spec.Containers[0],
		"volume": daemon.Spec.Template.Spec.Containers[0],
		"filer":  filer.Spec.Template.Spec.Containers[0],
	} {
		require.NotEmpty(t, container.Resources.Requests, "%s declares requests", name)
		require.NotEmpty(t, container.Resources.Limits, "%s declares limits", name)
		require.NotNil(t, container.LivenessProbe, "%s has a liveness probe", name)
		require.NotNil(t, container.StartupProbe, "%s has a startup probe", name)
		require.Equal(t, "/healthz", container.LivenessProbe.HTTPGet.Path)
		require.NotNil(t, container.ReadinessProbe, "%s has a readiness probe", name)
	}
	require.EqualValues(t, S3Port, filer.Spec.Template.Spec.Containers[0].ReadinessProbe.HTTPGet.Port.IntValue(),
		"the filer is ready when its S3 gateway answers")
	require.Equal(t, appsv1.RollingUpdateDeploymentStrategyType, filer.Spec.Strategy.Type)
	require.Equal(t, 0, filer.Spec.Strategy.RollingUpdate.MaxUnavailable.IntValue())
	require.Equal(t, 1, filer.Spec.Strategy.RollingUpdate.MaxSurge.IntValue())

	require.Len(t, budgets, 2)
	require.Equal(t, 1, budgets[MasterService].Spec.MaxUnavailable.IntValue())
	require.Equal(t, 1, budgets[FilerService].Spec.MinAvailable.IntValue())
	require.Contains(t, config.Data["master.toml"], "volume.fix.replication -apply")

	// A single master has no budget: zero tolerated disruptions would
	// only block drains. Two filers on two nodes keep theirs.
	two := RenderProduction(StoreSpec{Namespace: "skali-platform", Masters: 1, Filers: 2, Replication: "001", Managed: true})
	require.Equal(t, []string{FilerService}, budgetNames(two), "only the filer budget on a single-master store")

	// A single node runs a single filer, so no filer budget either; an
	// unset filer count renders the single-node shape.
	one := RenderProduction(StoreSpec{Namespace: "skali-platform", Masters: 1, Replication: "000", Managed: true})
	require.Empty(t, budgetNames(one), "no budget can tolerate a disruption on a single node")
	for _, object := range one {
		if deployment, ok := object.(*appsv1.Deployment); ok {
			require.EqualValues(t, 1, *deployment.Spec.Replicas)
		}
	}
}

func budgetNames(objects []runtime.Object) []string {
	var names []string
	for _, object := range objects {
		if budget, ok := object.(*policyv1.PodDisruptionBudget); ok {
			names = append(names, budget.Name)
		}
	}
	return names
}

// TestRenderPlacementIsRequired: masters and filers spread across nodes
// by a required topology spread (skew one, hostname, taints honored), not
// a preference, so replicas never silently share a node; a single node
// stays schedulable because one domain has no skew, and a rollout can
// surge on a full fleet because two against one is within the skew.
func TestRenderPlacementIsRequired(t *testing.T) {
	t.Parallel()
	objects := RenderProduction(StoreSpec{Namespace: "skali-platform", Masters: 3, Filers: 2, Replication: "001", Managed: true})
	checked := 0
	for _, object := range objects {
		var template corev1.PodSpec
		var app string
		switch typed := object.(type) {
		case *appsv1.StatefulSet:
			template, app = typed.Spec.Template.Spec, MasterService
		case *appsv1.Deployment:
			template, app = typed.Spec.Template.Spec, FilerService
		default:
			continue
		}
		checked++
		require.Nil(t, template.Affinity, "%s: no preferred placement left", app)
		require.Len(t, template.TopologySpreadConstraints, 1, app)
		spread := template.TopologySpreadConstraints[0]
		require.EqualValues(t, 1, spread.MaxSkew, app)
		require.Equal(t, "kubernetes.io/hostname", spread.TopologyKey, app)
		require.Equal(t, corev1.DoNotSchedule, spread.WhenUnsatisfiable, app)
		require.Equal(t, map[string]string{"app": app}, spread.LabelSelector.MatchLabels)
		require.NotNil(t, spread.NodeTaintsPolicy, app)
		require.Equal(t, corev1.NodeInclusionPolicyHonor, *spread.NodeTaintsPolicy, app)
	}
	require.Equal(t, 2, checked, "masters and filers both carry the rule")
}
