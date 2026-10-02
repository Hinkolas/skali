package compiler

import (
	"fmt"
	"github.com/Hinkolas/skali/internal/edge"
	"github.com/Hinkolas/skali/internal/utils"
)

type RouteError struct{ Field, Detail string }

func (e *RouteError) Error() string { return e.Field + ": " + e.Detail }

// ResolvedRoute is one hostname the environment claims: an application
// route (Application and Key set) or a bucket route (Bucket set, Key
// "route"). Field names the manifest path for diagnostics.
type ResolvedRoute struct {
	Application string
	Bucket      string
	Key         string
	Domain      string
	Path        string
	Field       string
}

// ResolveRoutes is the sole runtime route validation boundary, shared by
// admission and rendering. Never includes a resolved secret in an error.
func ResolveRoutes(def ProjectDefinition, variables map[string]string) ([]ResolvedRoute, error) {
	var routes []ResolvedRoute
	seen := map[string]string{}
	appDomains := map[string]string{}
	for _, app := range utils.SortedKeys(def.Applications) {
		for _, key := range utils.SortedKeys(def.Applications[app].Routes) {
			route := def.Applications[app].Routes[key]
			field := "applications." + app + ".routes." + key
			domain, err := resolveRouteDomain(route.Domain, field, variables)
			if err != nil {
				return nil, err
			}
			if err := edge.ValidatePath(route.Path); err != nil {
				return nil, &RouteError{field + ".path", err.Error()}
			}
			pair := domain + "\x00" + route.Path
			if prior, ok := seen[pair]; ok {
				return nil, &RouteError{field, fmt.Sprintf("conflicts with %s after resolving values", prior)}
			}
			seen[pair] = field
			if _, ok := appDomains[domain]; !ok {
				appDomains[domain] = field
			}
			routes = append(routes, ResolvedRoute{Application: app, Key: key, Domain: domain, Path: route.Path, Field: field})
		}
	}
	// Bucket routes may share a hostname with each other (the edge keys
	// each on its own bucket path) but not with an application route.
	for _, key := range utils.SortedKeys(def.Buckets) {
		route := def.Buckets[key].Route
		if route == nil {
			continue
		}
		field := "buckets." + key + ".route"
		domain, err := resolveRouteDomain(route.Domain, field, variables)
		if err != nil {
			return nil, err
		}
		if prior, ok := appDomains[domain]; ok {
			return nil, &RouteError{field, fmt.Sprintf("conflicts with %s after resolving values; a bucket cannot share a hostname with an application route", prior)}
		}
		routes = append(routes, ResolvedRoute{Bucket: key, Key: "route", Domain: domain, Path: "/", Field: field})
	}
	return routes, nil
}

// resolveRouteDomain resolves and canonicalizes one route domain
// expression. A rejected domain names the value it resolved from; the
// resolved text itself stays out of the error.
func resolveRouteDomain(expression Expression, field string, variables map[string]string) (string, error) {
	raw, err := ResolveExpression(expression, variables)
	if err != nil {
		return "", &RouteError{field + ".domain", err.Error()}
	}
	domain, err := edge.CanonicalDomain(raw)
	if err != nil {
		detail := err.Error()
		if expression.HasReferences() {
			detail += " (resolved from " + expression.Source() + ")"
		}
		return "", &RouteError{field + ".domain", detail}
	}
	return domain, nil
}
