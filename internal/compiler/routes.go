package compiler

import (
	"fmt"
	"github.com/Hinkolas/skali/internal/edge"
	"github.com/Hinkolas/skali/internal/utils"
)

type RouteError struct{ Field, Detail string }

func (e *RouteError) Error() string { return e.Field + ": " + e.Detail }

type ResolvedRoute struct{ Application, Key, Domain, Path, Field string }

// ResolveRoutes is the sole runtime route validation boundary, shared by
// admission and rendering. Never includes a resolved secret in an error.
func ResolveRoutes(def ProjectDefinition, variables map[string]string) ([]ResolvedRoute, error) {
	var routes []ResolvedRoute
	seen := map[string]string{}
	for _, app := range utils.SortedKeys(def.Applications) {
		for _, key := range utils.SortedKeys(def.Applications[app].Routes) {
			route := def.Applications[app].Routes[key]
			field := "applications." + app + ".routes." + key
			raw, err := ResolveExpression(route.Domain, variables)
			if err != nil {
				return nil, &RouteError{field + ".domain", err.Error()}
			}
			domain, err := edge.CanonicalDomain(raw)
			if err != nil {
				detail := err.Error()
				// Name where the value came from, so a rejected domain can
				// be traced to the value it resolved from; the resolved text
				// itself stays out of the error.
				if route.Domain.HasReferences() {
					detail += " (resolved from " + route.Domain.Source() + ")"
				}
				return nil, &RouteError{field + ".domain", detail}
			}
			if err := edge.ValidatePath(route.Path); err != nil {
				return nil, &RouteError{field + ".path", err.Error()}
			}
			pair := domain + "\x00" + route.Path
			if prior, ok := seen[pair]; ok {
				return nil, &RouteError{field, fmt.Sprintf("conflicts with %s after resolving values", prior)}
			}
			seen[pair] = field
			routes = append(routes, ResolvedRoute{app, key, domain, route.Path, field})
		}
	}
	return routes, nil
}
