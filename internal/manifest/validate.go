package manifest

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Hinkolas/skali/internal/naming"
	"github.com/Hinkolas/skali/internal/utils"
	"github.com/Hinkolas/skali/internal/version"
	"github.com/Hinkolas/skali/internal/yamldoc"
)

func Validate(document *Document) yamldoc.Diagnostics {
	project := document.Project
	var diagnostics yamldoc.Diagnostics
	add := func(path, format string, args ...any) {
		diagnostics = append(diagnostics, document.Diagnostic(path, fmt.Sprintf(format, args...)))
	}

	watermark, ok := Watermark(project.Skali)
	switch {
	case strings.TrimSpace(project.Skali) == "":
		add("skali", "is required: the skali release this manifest was last reviewed against, for example skali: %s", ReferenceRelease())
	case !ok:
		add("skali", "%q is not a skali release; expected a tag like %s", project.Skali, ReferenceRelease())
	case version.IsRelease(version.Version) && version.Older(version.Version, watermark):
		add("skali", "reviewed against %s, newer than this compiler (%s); select that release or review this release's references and explicitly edit the watermark to acknowledge the older target", watermark, version.Version)
	default:
		validateLedger(&diagnostics, document, Ledger, watermark)
	}
	if err := naming.CheckKey(project.Name); err != nil {
		add("name", "%s", err)
	}
	if len(project.Applications)+len(project.Databases)+len(project.Buckets) == 0 {
		add("", "must declare at least one application, database, or bucket")
	}

	for _, key := range utils.SortedKeys(project.Applications) {
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
		seenPlatforms := make(map[string]struct{}, len(application.Platforms))
		for index, platform := range application.Platforms {
			platformPath := fmt.Sprintf("%s.platforms[%d]", path, index)
			if !slices.Contains(KnownPlatforms, platform) {
				add(platformPath, "unknown platform %q; supported platforms are %s", platform, strings.Join(KnownPlatforms, ", "))
				continue
			}
			if _, ok := seenPlatforms[platform]; ok {
				add(platformPath, "duplicate platform %q", platform)
			}
			seenPlatforms[platform] = struct{}{}
		}
		for _, portKey := range utils.SortedKeys(application.Ports) {
			portPath := path + ".ports." + portKey
			validateStableKey(&diagnostics, document, portPath, portKey)
			port := application.Ports[portKey]
			if port.Port < 1 || port.Port > 65535 {
				add(portPath+".port", "must be between 1 and 65535")
			}
		}
		for _, routeKey := range utils.SortedKeys(application.Routes) {
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
		for _, commandKey := range utils.SortedKeys(application.Commands) {
			commandPath := path + ".commands." + commandKey
			validateStableKey(&diagnostics, document, commandPath, commandKey)
			if len(application.Commands[commandKey]) == 0 {
				add(commandPath, "must not be empty")
			}
		}
		if document.Has(path + ".dev") {
			if len(application.Dev.Command) == 0 {
				add(path+".dev.command", "is required")
			}
			for _, portKey := range utils.SortedKeys(application.Dev.Ports) {
				portPath := path + ".dev.ports." + portKey
				if _, ok := application.Ports[portKey]; !ok {
					add(portPath, "references unknown application port %q", portKey)
				}
				if hostPort := application.Dev.Ports[portKey]; hostPort < 1 || hostPort > 65535 {
					add(portPath, "must be between 1 and 65535")
				}
			}
		}
	}

	for _, key := range utils.SortedKeys(project.Databases) {
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

	for _, key := range utils.SortedKeys(project.Buckets) {
		validateStableKey(&diagnostics, document, "buckets."+key, key)
	}

	for _, key := range utils.SortedKeys(project.Backups) {
		path := "backups." + key
		backup := project.Backups[key]
		validateStableKey(&diagnostics, document, path, key)
		if backup.Schedule == "" {
			add(path+".schedule", "is required")
		}
		if backup.Retention == "" {
			add(path+".retention", "is required")
		}
		if backup.Strategy != "" && backup.Strategy != StrategyComplete {
			add(path+".strategy", "must be %q (the only strategy today)", StrategyComplete)
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
		// "all" of a class the manifest does not declare selects nothing; a
		// policy that would snapshot nothing is a mistake, not a schedule.
		if !selectionMatches(backup.Include.Databases, len(project.Databases)) &&
			!selectionMatches(backup.Include.Buckets, len(project.Buckets)) &&
			!selectionMatches(backup.Include.Volumes, countVolumes(project)) {
			add(path+".include", "includes nothing the manifest declares (no database, bucket, or volume matches)")
		}
	}

	return diagnostics
}

func validateStableKey(diagnostics *yamldoc.Diagnostics, document *Document, path, key string) {
	if err := naming.CheckKey(key); err != nil {
		*diagnostics = append(*diagnostics, document.Diagnostic(path, "key "+err.Error()))
	}
}

func validateSelection[T any](diagnostics *yamldoc.Diagnostics, document *Document, path string, selection Selection, resources map[string]T) {
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

// selectionMatches reports whether the selection names at least one
// resource of a class with declared members. Named keys are checked for
// existence separately; here a key list counts as a match.
func selectionMatches(selection Selection, declared int) bool {
	if selection.All {
		return declared > 0
	}
	return len(selection.Keys) > 0
}

func countVolumes(project Project) int {
	total := 0
	for _, application := range project.Applications {
		total += len(application.Volumes)
	}
	return total
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

// validateLedger applies the changed entries: a manifest that writes a path
// whose meaning moved after its watermark fails until the watermark moves
// past the change (docs/versioning.md, decision 4). Removed entries are
// handled while parsing, where the unknown field surfaces; added entries
// are silent.
func validateLedger(diagnostics *yamldoc.Diagnostics, document *Document, ledger []Change, watermark string) {
	paths := document.Paths()
	for _, change := range ledger {
		if change.Kind != ChangeChanged || !version.Older(watermark, change.Release) {
			continue
		}
		matched := map[string]bool{}
		for _, path := range paths {
			if !change.WhenOmitted && change.Matches(path) {
				matched[path] = true
			}
		}
		if change.WhenOmitted {
			// Expand wildcards against declared resources, then append any omitted
			// fixed suffix (including omitted parent objects such as deployment).
			prefix, suffix := "", change.Path
			if i := strings.LastIndex(change.Path, "*"); i >= 0 {
				prefix, suffix = change.Path[:i+1], change.Path[i+1:]
			}
			if prefix == "" {
				if !document.Has(change.Path) {
					matched[change.Path] = true
				}
			} else {
				pattern := Change{Path: prefix}
				for _, path := range paths {
					if pattern.Matches(path) && !document.Has(path+suffix) {
						matched[path+suffix] = true
					}
				}
			}
		}
		for _, path := range utils.SortedKeys(matched) {
			*diagnostics = append(*diagnostics, document.Diagnostic(path, fmt.Sprintf(
				"%s (changed in %s; this manifest was reviewed against %s); %s, then explicitly review and edit skali: to acknowledge",
				change.Message, change.Release, watermark, change.Hint)))
		}
	}
}

// ReviewChanges evaluates meaning/default changes against the original review
// point before a command can replace that point with a newer watermark.
func ReviewChanges(document *Document, watermark string) yamldoc.Diagnostics {
	var diagnostics yamldoc.Diagnostics
	validateLedger(&diagnostics, document, Ledger, watermark)
	return diagnostics
}
