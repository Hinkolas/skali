package compiler

import "sort"

// ServiceConsumers lists the applications whose environment references an
// output of the service (collection "databases" or "buckets", the manifest
// key), sorted by key. Rotating the service's credentials rolls exactly
// these applications; nothing else holds the keys.
func ServiceConsumers(definition *ProjectDefinition, collection, service string) []string {
	if definition == nil {
		return nil
	}
	var consumers []string
	for key, application := range definition.Applications {
		if referencesService(application, collection, service) {
			consumers = append(consumers, key)
		}
	}
	sort.Strings(consumers)
	return consumers
}

func referencesService(application Application, collection, service string) bool {
	for _, expression := range application.Environment {
		for _, part := range expression.Parts {
			if part.Kind == "service_output" && part.Collection == collection && part.Service == service {
				return true
			}
		}
	}
	return false
}
