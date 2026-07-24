package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/layout"
)

func TestRenderClusterPlanUsesStructuredRows(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	renderClusterPlan(&out, clusterstate.Plan{
		FromRevision:     "cc227fde-fc21-4356-a4e5-504715125b80",
		TargetRevision:   "71d4df4c-f8d2-40ac-8697-daf74ad4614f",
		DatabaseTierFrom: layout.TierSingle,
		DatabaseTierTo:   layout.TierSingle,
		Actions: []clusterstate.Action{{
			Kind:   clusterstate.ActionReconcilePlatform,
			Detail: "reconcile platform once at database tier single",
		}},
	})

	rendered := out.String()
	require.Contains(t, rendered, "◆ Cluster plan\n")
	require.Contains(t, rendered, "  from       cc227fde\n")
	require.Contains(t, rendered, "  target     71d4df4c\n")
	require.Contains(t, rendered, "  actions\n")
	require.Contains(t, rendered, "    ○ reconcile platform\n")
	require.Contains(t, rendered, "      reconcile platform once at database tier single\n")
	require.NotContains(t, rendered, "cc227fde-fc21")
}
