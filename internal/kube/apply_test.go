package kube

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kt "k8s.io/client-go/testing"
)

func TestApplyRetriesFreshVersion(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-op", true: "changed"}[changed], func(t *testing.T) {
			want := ownedSecret(uuid.NewString())
			want.SetGeneration(1)
			client, dynamic := ownershipClient(want)
			calls := 0
			dynamic.PrependReactor("patch", "secrets", func(action kt.Action) (bool, runtime.Object, error) {
				calls++
				patch := action.(kt.PatchAction)
				require.False(t, *action.(kt.PatchActionImpl).GetPatchOptions().Force)
				applied := &unstructured.Unstructured{}
				require.NoError(t, json.Unmarshal(patch.GetPatch(), applied))
				live := want.DeepCopy()
				live.SetResourceVersion("8")
				if calls == 1 {
					require.Equal(t, "7", applied.GetResourceVersion())
					require.NoError(t, dynamic.Tracker().Update(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, live, live.GetNamespace()))
					return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, live.GetName(), errors.New("object modified"))
				}
				require.Equal(t, "8", applied.GetResourceVersion())
				if changed {
					live.SetGeneration(2)
					live.SetResourceVersion("9")
				}
				return true, live, nil
			})
			result, err := client.Apply(context.Background(), want, false)
			require.NoError(t, err)
			require.Equal(t, changed, result.Changed)
			require.Equal(t, 2, calls)
			require.Equal(t, "7", want.GetResourceVersion(), "input must not be mutated")
		})
	}
}

func TestApplyRetryBoundAndPermanentErrors(t *testing.T) {
	conflict := apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, "values", errors.New("object modified"))
	fieldConflict := &apierrors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonConflict, Code: 409, Details: &metav1.StatusDetails{Causes: []metav1.StatusCause{{Type: metav1.CauseTypeFieldManagerConflict}}}}}
	for _, tc := range []struct {
		name  string
		err   error
		calls int
	}{
		{"stale version", conflict, 5}, {"field owner", fieldConflict, 1},
		{"forbidden", apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "values", errors.New("denied")), 1},
		{"invalid", apierrors.NewBadRequest("invalid"), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := ownedSecret(uuid.NewString())
			client, dynamic := ownershipClient(want)
			calls := 0
			dynamic.PrependReactor("patch", "secrets", func(kt.Action) (bool, runtime.Object, error) { calls++; return true, nil, tc.err })
			_, err := client.Apply(context.Background(), want, false)
			require.ErrorIs(t, err, tc.err)
			require.Equal(t, tc.calls, calls)
		})
	}
}

func TestApplyRevalidatesIdentityAfterConflict(t *testing.T) {
	for _, change := range []string{"uid", "owner", "deleted"} {
		t.Run(change, func(t *testing.T) {
			want := ownedSecret(uuid.NewString())
			client, dynamic := ownershipClient(want)
			calls := 0
			dynamic.PrependReactor("patch", "secrets", func(kt.Action) (bool, runtime.Object, error) {
				calls++
				live := want.DeepCopy()
				gvr := schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
				switch change {
				case "uid":
					live.SetUID("replacement")
				case "owner":
					live.SetLabels(nil)
				case "deleted":
					require.NoError(t, dynamic.Tracker().Delete(gvr, live.GetNamespace(), live.GetName()))
				}
				if change != "deleted" {
					require.NoError(t, dynamic.Tracker().Update(gvr, live, live.GetNamespace()))
				}
				return true, nil, apierrors.NewConflict(gvr.GroupResource(), live.GetName(), errors.New("object modified"))
			})
			_, err := client.Apply(context.Background(), want, false)
			require.Error(t, err)
			require.Equal(t, 1, calls)
		})
	}
}

func TestApplyCreationRaceIsBounded(t *testing.T) {
	want := ownedSecret(uuid.NewString())
	client, dynamic := ownershipClient()
	calls := 0
	dynamic.PrependReactor("create", "secrets", func(kt.Action) (bool, runtime.Object, error) {
		calls++
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "secrets"}, want.GetName())
	})
	_, err := client.Apply(context.Background(), want, false)
	require.True(t, apierrors.IsAlreadyExists(err))
	require.Equal(t, 5, calls)
}

func TestApplyCancellationStopsRetry(t *testing.T) {
	want := ownedSecret(uuid.NewString())
	client, dynamic := ownershipClient(want)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	dynamic.PrependReactor("patch", "secrets", func(kt.Action) (bool, runtime.Object, error) {
		calls++
		cancel()
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, want.GetName(), nil)
	})
	_, err := client.Apply(ctx, want, false)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, calls)
}

func TestApplyProtectedIdentityAndFallback(t *testing.T) {
	for _, mode := range []string{"active", "invalidated", "expired"} {
		t.Run(mode, func(t *testing.T) {
			want := ownedSecret(uuid.NewString())
			client, ambient := ownershipClient(want)
			peer, verified := ownershipClient(want)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			guard := &ownershipProtection{server: peer, context: ctx}
			guard.valid.Store(mode != "invalidated")
			if mode == "expired" {
				cancel()
			}
			client.ownership.Store(guard)
			ambientCalls, verifiedCalls := 0, 0
			ambient.PrependReactor("patch", "secrets", func(action kt.Action) (bool, runtime.Object, error) {
				ambientCalls++
				applied := &unstructured.Unstructured{}
				require.NoError(t, json.Unmarshal(action.(kt.PatchAction).GetPatch(), applied))
				require.Equal(t, "7", applied.GetResourceVersion())
				require.Empty(t, applied.GetUID())
				return true, want, nil
			})
			verified.PrependReactor("patch", "secrets", func(action kt.Action) (bool, runtime.Object, error) {
				verifiedCalls++
				applied := &unstructured.Unstructured{}
				require.NoError(t, json.Unmarshal(action.(kt.PatchAction).GetPatch(), applied))
				require.Empty(t, applied.GetResourceVersion())
				require.Equal(t, want.GetUID(), applied.GetUID())
				return true, want, nil
			})
			result, err := client.Apply(context.Background(), want, false)
			require.NoError(t, err)
			require.False(t, result.Changed)
			if mode == "active" {
				require.Equal(t, 1, verifiedCalls)
				require.Zero(t, ambientCalls)
			} else {
				require.Equal(t, 1, ambientCalls)
				require.Zero(t, verifiedCalls)
			}
		})
	}
}
