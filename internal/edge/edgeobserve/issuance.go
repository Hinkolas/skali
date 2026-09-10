package edgeobserve

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"

	"github.com/Hinkolas/skali/internal/edge"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/module"
)

// RetryFailedCertificate uses the same Issuing condition transition as
// cmctl renew. cert-manager owns replacing the failed request and order.
// Only failures older than this promotion qualify. The Issuing transition
// and subsequent lastFailureTime make this restart-safe without using the
// explanatory journal as a retry ledger. Fresh reads and resourceVersion
// protect a concurrent controller update; a usable certificate is untouched.
func RetryFailedCertificate(ctx context.Context, client dynamic.Interface, ref kube.ObjectRef, promoted time.Time) (bool, error) {
	changed := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		resource := client.Resource(edge.CertificateGVR).Namespace(ref.Namespace)
		obj, err := resource.Get(ctx, ref.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if ref.UID == "" || obj.GetUID() != ref.UID || obj.GetDeletionTimestamp() != nil {
			return nil
		}
		projected, _ := ConvertCertificate(obj)
		status := projected.Certificate
		now := time.Now().UTC()
		if status.Ready || status.Issuing || status.NotAfter.After(now) || status.LastFailureTime.IsZero() || !status.LastFailureTime.Before(promoted.Truncate(time.Second)) {
			return nil
		}
		// Timestamp precision is seconds in cert-manager. Wait until the new
		// transition is strictly newer than failure, or requestmanager cannot
		// distinguish the old failed request from the new issuance.
		if !now.Truncate(time.Second).After(status.LastFailureTime) {
			return nil
		}
		conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
		condition := map[string]any{"type": "Issuing", "status": "True", "reason": "ManuallyTriggered", "message": "Certificate re-issuance triggered by skali redeploy", "lastTransitionTime": now.Format(time.RFC3339), "observedGeneration": obj.GetGeneration()}
		found := false
		for i, raw := range conditions {
			if c, ok := raw.(map[string]any); ok && c["type"] == "Issuing" {
				conditions[i] = condition
				found = true
			}
		}
		if !found {
			conditions = append(conditions, condition)
		}
		if err := unstructured.SetNestedSlice(obj.Object, conditions, "status", "conditions"); err != nil {
			return err
		}
		_, err = resource.UpdateStatus(ctx, obj, metav1.UpdateOptions{})
		if err == nil {
			changed = true
		}
		return err
	})
	return changed, err
}

// InspectCertificate reads only public issuance metadata. The owner UID
// chain prevents old or unrelated requests/orders from explaining this
// route. No Secret data, CSR, challenge token or private key is read.
func InspectCertificate(ctx context.Context, client dynamic.Interface, ref kube.ObjectRef) (*module.CertificateStatus, map[string]string, error) {
	obj, err := client.Resource(edge.CertificateGVR).Namespace(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}
	if ref.UID != "" && obj.GetUID() != ref.UID {
		return nil, nil, fmt.Errorf("certificate was replaced; waiting for observation")
	}
	projected, _ := ConvertCertificate(obj)
	status := projected.Certificate
	details := map[string]string{}
	requests, err := owned(ctx, client, schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificaterequests"}, obj)
	if err != nil {
		return status, details, err
	}
	revision, _, _ := unstructured.NestedInt64(obj.Object, "status", "revision")
	for _, request := range requests {
		// Only the next issuance revision is relevant while waiting. Never
		// display a previous successful issuance as the current attempt.
		n, _ := strconv.ParseInt(request.GetAnnotations()["cert-manager.io/certificate-revision"], 10, 64)
		if n != revision+1 {
			continue
		}
		details["request"] = request.GetName()
		details["request_status"] = conditionDetail(request)
		orders, err := owned(ctx, client, schema.GroupVersionResource{Group: "acme.cert-manager.io", Version: "v1", Resource: "orders"}, request)
		if err != nil {
			return status, details, err
		}
		if len(orders) == 0 {
			break
		}
		order := orders[0]
		details["order"] = order.GetName()
		details["order_state"], _, _ = unstructured.NestedString(order.Object, "status", "state")
		details["order_reason"], _, _ = unstructured.NestedString(order.Object, "status", "reason")
		challenges, err := owned(ctx, client, schema.GroupVersionResource{Group: "acme.cert-manager.io", Version: "v1", Resource: "challenges"}, order)
		if err != nil {
			return status, details, err
		}
		lines := []string{}
		for _, challenge := range challenges {
			host, _, _ := unstructured.NestedString(challenge.Object, "spec", "dnsName")
			state, _, _ := unstructured.NestedString(challenge.Object, "status", "state")
			reason, _, _ := unstructured.NestedString(challenge.Object, "status", "reason")
			presented, _, _ := unstructured.NestedBool(challenge.Object, "status", "presented")
			processing, _, _ := unstructured.NestedBool(challenge.Object, "status", "processing")
			lines = append(lines, fmt.Sprintf("%s (%s): %s; presented=%t, processing=%t\n%s", host, challenge.GetName(), state, presented, processing, reason))
		}
		details["challenges"] = strings.Join(lines, "\n")
		break
	}
	return status, details, nil
}

func owned(ctx context.Context, client dynamic.Interface, gvr schema.GroupVersionResource, parent *unstructured.Unstructured) ([]*unstructured.Unstructured, error) {
	list, err := client.Resource(gvr).Namespace(parent.GetNamespace()).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", gvr.Resource, err)
	}
	var result []*unstructured.Unstructured
	for i := range list.Items {
		item := &list.Items[i]
		if ownedBy(item, parent.GetUID()) {
			result = append(result, item)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i].GetCreationTimestamp(), result[j].GetCreationTimestamp()
		if a.Equal(&b) {
			return result[i].GetName() < result[j].GetName()
		}
		return a.After(b.Time)
	})
	return result, nil
}
func ownedBy(obj *unstructured.Unstructured, uid types.UID) bool {
	if uid == "" {
		return false
	}
	for _, owner := range obj.GetOwnerReferences() {
		if owner.UID == uid && owner.Controller != nil && *owner.Controller {
			return true
		}
	}
	return false
}
func conditionDetail(obj *unstructured.Unstructured) string {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	var parts []string
	for _, raw := range conditions {
		if c, ok := raw.(map[string]any); ok {
			kind, _ := c["type"].(string)
			state, _ := c["status"].(string)
			reason, _ := c["reason"].(string)
			message, _ := c["message"].(string)
			parts = append(parts, strings.TrimSpace(kind+"="+state+": "+reason+": "+message))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n")
}
