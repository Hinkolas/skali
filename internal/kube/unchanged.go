package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"k8s.io/kube-openapi/pkg/handler3"
	"k8s.io/kube-openapi/pkg/spec3"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
	"sigs.k8s.io/structured-merge-diff/v6/typed"
	"sigs.k8s.io/structured-merge-diff/v6/value"
)

// How long a group version's schema is trusted before Apply asks the server
// whether it changed (a CRD upgrade), and how long an unavailable one is
// left before the next try.
const (
	schemaRecheck = 10 * time.Minute
	schemaRetry   = time.Minute
)

// applySchemas holds, per group version, the structural schema the API
// server applies with, read from its OpenAPI v3 document, so Apply can
// recognize an apply that would change nothing without sending it.
type applySchemas struct {
	mu      sync.Mutex
	entries map[schema.GroupVersion]schemaEntry
}

type schemaEntry struct {
	converter managedfields.TypeConverter // nil while unavailable
	url       string                      // the schema document, hash included
	checked   time.Time
}

// converter returns the type converter for gv, or nil when the server's
// schema for it is unavailable; Apply then always sends the apply.
func (c *Client) converter(ctx context.Context, gv schema.GroupVersion) managedfields.TypeConverter {
	c.schemas.mu.Lock()
	entry, known := c.schemas.entries[gv]
	c.schemas.mu.Unlock()
	wait := schemaRecheck
	if entry.converter == nil {
		wait = schemaRetry
	}
	if known && time.Since(entry.checked) < wait || c.Clientset == nil {
		return entry.converter
	}

	refreshed, err := c.loadSchema(ctx, gv, entry)
	if err != nil {
		if ctx.Err() != nil {
			return entry.converter
		}
		// A schema that cannot be checked right now most likely still
		// holds; one never read stays unavailable until the retry.
		refreshed = schemaEntry{converter: entry.converter, url: entry.url}
	}
	refreshed.checked = time.Now()
	c.schemas.mu.Lock()
	if c.schemas.entries == nil {
		c.schemas.entries = map[schema.GroupVersion]schemaEntry{}
	}
	c.schemas.entries[gv] = refreshed
	c.schemas.mu.Unlock()
	return refreshed.converter
}

// loadSchema finds gv's schema document and builds its converter, keeping
// the current one when the document is unchanged.
func (c *Client) loadSchema(ctx context.Context, gv schema.GroupVersion, current schemaEntry) (schemaEntry, error) {
	client := c.Clientset.Discovery().RESTClient()
	if client == nil {
		return schemaEntry{}, errors.New("kube: no discovery client")
	}
	raw, err := client.Get().AbsPath("/openapi/v3").Do(ctx).Raw()
	if err != nil {
		return schemaEntry{}, err
	}
	var discovery handler3.OpenAPIV3Discovery
	if err := json.Unmarshal(raw, &discovery); err != nil {
		return schemaEntry{}, err
	}
	path := "apis/" + gv.Group + "/" + gv.Version
	if gv.Group == "" {
		path = "api/" + gv.Version
	}
	item, ok := discovery.Paths[path]
	if !ok {
		return schemaEntry{}, fmt.Errorf("kube: no OpenAPI v3 schema for %s", gv)
	}
	if current.converter != nil && current.url == item.ServerRelativeURL {
		return current, nil
	}
	locator, err := url.Parse(item.ServerRelativeURL)
	if err != nil {
		return schemaEntry{}, err
	}
	request := client.Get().AbsPath(locator.Path).SetHeader("Accept", "application/json")
	for key, values := range locator.Query() {
		for _, value := range values {
			request.Param(key, value)
		}
	}
	raw, err = request.Do(ctx).Raw()
	if err != nil {
		return schemaEntry{}, err
	}
	var document spec3.OpenAPI
	if err := json.Unmarshal(raw, &document); err != nil {
		return schemaEntry{}, err
	}
	if document.Components == nil {
		return schemaEntry{}, fmt.Errorf("kube: OpenAPI v3 schema for %s has no components", gv)
	}
	converter, err := managedfields.NewTypeConverter(document.Components.Schemas, false)
	if err != nil {
		return schemaEntry{}, err
	}
	return schemaEntry{converter: converter, url: item.ServerRelativeURL}, nil
}

// strippedFields are the fields the server's field manager never records
// as owned.
var strippedFields = fieldpath.NewSet(
	fieldpath.MakePathOrDie("apiVersion"),
	fieldpath.MakePathOrDie("kind"),
	fieldpath.MakePathOrDie("metadata"),
	fieldpath.MakePathOrDie("metadata", "name"),
	fieldpath.MakePathOrDie("metadata", "namespace"),
	fieldpath.MakePathOrDie("metadata", "creationTimestamp"),
	fieldpath.MakePathOrDie("metadata", "selfLink"),
	fieldpath.MakePathOrDie("metadata", "uid"),
	fieldpath.MakePathOrDie("metadata", "clusterName"),
	fieldpath.MakePathOrDie("metadata", "generation"),
	fieldpath.MakePathOrDie("metadata", "managedFields"),
	fieldpath.MakePathOrDie("metadata", "resourceVersion"),
)

// unchangedBy reports whether a server-side apply of applied by manager
// would leave live exactly as it is. The server's field manager merges the
// configuration into the live object and then prunes the fields the
// manager owned before but no longer applies, so the apply is a no-op when
// the manager already owns exactly the configuration's fields, at the same
// API version, and the merge changes no value. Any doubt reports false and
// the apply goes to the server.
func unchangedBy(converter managedfields.TypeConverter, applied, live *unstructured.Unstructured, manager string) bool {
	var owned *metav1.ManagedFieldsEntry
	entries := live.GetManagedFields()
	for index, entry := range entries {
		if entry.Manager == manager && entry.Operation == metav1.ManagedFieldsOperationApply && entry.Subresource == "" {
			owned = &entries[index]
			break
		}
	}
	if owned == nil || owned.APIVersion != applied.GetAPIVersion() || owned.FieldsV1 == nil {
		return false
	}
	ownedSet := &fieldpath.Set{}
	if err := ownedSet.FromJSON(bytes.NewReader(owned.FieldsV1.Raw)); err != nil {
		return false
	}
	config, err := converter.ObjectToTyped(applied)
	if err != nil {
		return false
	}
	configSet, err := config.ToFieldSet()
	if err != nil || !configSet.Difference(strippedFields).Equals(ownedSet) {
		return false
	}
	current, err := converter.ObjectToTyped(live, typed.AllowDuplicates)
	if err != nil {
		return false
	}
	merged, err := current.Merge(config)
	if err != nil {
		return false
	}
	return value.Equals(current.AsValue(), merged.AsValue())
}
