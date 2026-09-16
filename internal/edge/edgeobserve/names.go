package edgeobserve

import (
	"context"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/metadata"
)

// secretsGVR addresses the TLS Secret a Certificate fills.
var secretsGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// cert-manager annotates the Secret it writes with the names the issued
// certificate covers; they are the ground truth for "which domain does the
// edge actually serve" after a route's domain changed on the same key.
const (
	annotationAltNames   = "cert-manager.io/alt-names"
	annotationCommonName = "cert-manager.io/common-name"
)

// IssuedNames reads the names the certificate in secretName was issued
// for, from cert-manager's annotations on the Secret. Only the object's
// metadata travels: no key material or certificate data is requested. A
// Secret that does not exist or carries no annotation yields nil, nil (no
// verdict), never an error.
func IssuedNames(ctx context.Context, client metadata.Interface, namespace, secretName string) ([]string, error) {
	if client == nil || secretName == "" {
		return nil, nil
	}
	object, err := client.Resource(secretsGVR).Namespace(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return namesFromAnnotations(object.GetAnnotations()), nil
}

// namesFromAnnotations folds the alt-names list and the common name into
// one sorted, lower-cased, deduplicated list; nil when neither is set.
func namesFromAnnotations(annotations map[string]string) []string {
	seen := map[string]bool{}
	var names []string
	add := func(raw string) {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}
	for _, raw := range strings.Split(annotations[annotationAltNames], ",") {
		add(raw)
	}
	add(annotations[annotationCommonName])
	sort.Strings(names)
	return names
}

// Covers reports whether a certificate issued for names serves domain: an
// exact match, case-insensitively, or a wildcard name standing in for
// exactly one label. An empty list covers nothing.
func Covers(names []string, domain string) bool {
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if domain == "" {
		return false
	}
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == domain {
			return true
		}
		if suffix, ok := strings.CutPrefix(name, "*."); ok {
			label, rest, found := strings.Cut(domain, ".")
			if found && label != "" && rest == suffix {
				return true
			}
		}
	}
	return false
}
