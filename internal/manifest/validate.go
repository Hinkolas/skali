package manifest

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
)

var stableKeyPattern = regexp.MustCompile("^[a-z][a-z0-9-]{0,62}$")

var valueNamePattern = regexp.MustCompile("^[A-Z_][A-Z0-9_]*$")

func Validate(document *Document) Diagnostics {
	project := document.Project
	var diagnostics Diagnostics
	add := func(path, format string, args ...any) {
		diagnostics = append(diagnostics, document.Diagnostic(path, fmt.Sprintf(format, args...)))
	}

	if project.Version != CurrentVersion {
		add("version", "unsupported manifest version %q; expected %q", project.Version, CurrentVersion)
	}
	if !stableKeyPattern.MatchString(project.Name) {
		add("name", "must start with a lowercase letter and contain only lowercase letters, numbers, and hyphens")
	}
	if len(project.Applications)+len(project.Databases)+len(project.Buckets) == 0 {
		add("", "must declare at least one application, database, or bucket")
	}

	for _, key := range sortedKeys(project.Values) {
		if !valueNamePattern.MatchString(key) {
			add("values."+key, "value names must be uppercase environment-variable names such as APP_DOMAIN")
		}
	}

	for _, key := range sortedKeys(project.Applications) {
		path := "applications." + key
		application := project.Applications[key]
		validateStableKey(&diagnostics, document, path, key)
		hasImage := application.Image != ""
		hasBuild := document.Has(path + ".build")
		if hasImage == hasBuild {
			add(path, "must declare exactly one of image or build")
		}
		if hasBuild && application.Build.Context == "" {
			add(path+".build.context", "is required")
		}
		for _, portKey := range sortedKeys(application.Ports) {
			portPath := path + ".ports." + portKey
			validateStableKey(&diagnostics, document, portPath, portKey)
			port := application.Ports[portKey]
			if port.Port < 1 || port.Port > 65535 {
				add(portPath+".port", "must be between 1 and 65535")
			}
		}
		for _, routeKey := range sortedKeys(application.Routes) {
			routePath := path + ".routes." + routeKey
			validateStableKey(&diagnostics, document, routePath, routeKey)
			route := application.Routes[routeKey]
			if route.Domain == "" {
				add(routePath+".domain", "is required")
			}
			if route.Port == "" {
				add(routePath+".port", "is required")
			} else if number, err := strconv.Atoi(string(route.Port)); err != nil {
				if _, ok := application.Ports[string(route.Port)]; !ok {
					add(routePath+".port", "references unknown application port %q", route.Port)
				}
			} else if number < 1 || number > 65535 {
				add(routePath+".port", "must be between 1 and 65535")
			}
		}
	}

	for _, key := range sortedKeys(project.Databases) {
		path := "databases." + key
		database := project.Databases[key]
		validateStableKey(&diagnostics, document, path, key)
		if database.Engine == "" {
			add(path+".engine", "is required")
		}
		if database.Version == "" {
			add(path+".version", "is required")
		}
	}

	for _, key := range sortedKeys(project.Buckets) {
		validateStableKey(&diagnostics, document, "buckets."+key, key)
	}

	for _, key := range sortedKeys(project.Backups) {
		path := "backups." + key
		backup := project.Backups[key]
		validateStableKey(&diagnostics, document, path, key)
		if backup.Schedule == "" {
			add(path+".schedule", "is required")
		}
		if backup.Retention == "" {
			add(path+".retention", "is required")
		}
		if selectionEmpty(backup.Include.Databases) && selectionEmpty(backup.Include.Buckets) && selectionEmpty(backup.Include.Volumes) {
			add(path+".include", "must include at least one resource class")
		}
		validateSelection(&diagnostics, document, path+".include.databases", backup.Include.Databases, project.Databases)
		validateSelection(&diagnostics, document, path+".include.buckets", backup.Include.Buckets, project.Buckets)
		if !backup.Include.Volumes.All {
			for _, volume := range backup.Include.Volumes.Keys {
				if !volumeExists(project, volume) {
					add(path+".include.volumes", "references unknown volume %q; use application.volume", volume)
				}
			}
		}
	}

	return diagnostics
}

func validateStableKey(diagnostics *Diagnostics, document *Document, path, key string) {
	if stableKeyPattern.MatchString(key) {
		return
	}
	*diagnostics = append(*diagnostics, document.Diagnostic(path,
		"key must start with a lowercase letter and contain only lowercase letters, numbers, and hyphens"))
}

func validateSelection[T any](diagnostics *Diagnostics, document *Document, path string, selection Selection, resources map[string]T) {
	if selection.All {
		return
	}
	for _, key := range selection.Keys {
		if _, ok := resources[key]; !ok {
			*diagnostics = append(*diagnostics, document.Diagnostic(path, fmt.Sprintf("references unknown resource %q", key)))
		}
	}
}

func selectionEmpty(selection Selection) bool {
	return !selection.All && len(selection.Keys) == 0
}

func volumeExists(project Project, reference string) bool {
	for applicationKey, application := range project.Applications {
		for volumeKey := range application.Volumes {
			if reference == applicationKey+"."+volumeKey {
				return true
			}
		}
	}
	return false
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
