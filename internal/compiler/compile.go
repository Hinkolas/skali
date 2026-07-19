package compiler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Hinkolas/skali/internal/manifest"
)

var environmentKeyPattern = regexp.MustCompile("^[A-Za-z_][A-Za-z0-9_]*$")

type builder struct {
	document     *manifest.Document
	diagnostics  manifest.Diagnostics
	variables    map[string]VariableRequirement
	variablePath map[string]string
	dependencies map[string]map[string]struct{}
	routes       map[string]string
}

func Compile(document *manifest.Document) (*Result, error) {
	if diagnostics := manifest.Validate(document); len(diagnostics) > 0 {
		return nil, diagnostics
	}
	b := &builder{
		document:     document,
		variables:    make(map[string]VariableRequirement),
		variablePath: make(map[string]string),
		dependencies: make(map[string]map[string]struct{}),
		routes:       make(map[string]string),
	}
	source := document.Project
	definition := ProjectDefinition{
		Version:      source.Version,
		Name:         source.Name,
		Description:  source.Description,
		Applications: make(map[string]Application, len(source.Applications)),
		Databases:    make(map[string]DatabaseClaim, len(source.Databases)),
		Buckets:      make(map[string]BucketClaim, len(source.Buckets)),
		Backups:      make(map[string]Backup, len(source.Backups)),
		Dependencies: make(map[string][]string, len(source.Applications)),
	}

	for _, key := range mapKeys(source.Applications) {
		definition.Applications[key] = b.compileApplication(key, source.Applications[key])
	}
	for _, key := range mapKeys(source.Databases) {
		definition.Databases[key] = b.compileDatabase(key, source.Databases[key])
	}
	for _, key := range mapKeys(source.Buckets) {
		definition.Buckets[key] = b.compileBucket(key, source.Buckets[key])
	}
	for _, key := range mapKeys(source.Backups) {
		definition.Backups[key] = b.compileBackup(key, source.Backups[key])
	}

	for _, name := range mapKeys(source.Values) {
		if _, used := b.variables[name]; !used {
			b.add("values."+name, "is declared but never referenced")
		}
	}
	for _, name := range mapKeys(b.variables) {
		definition.RequiredVariables = append(definition.RequiredVariables, b.variables[name])
	}
	for _, owner := range mapKeys(b.dependencies) {
		if len(b.dependencies[owner]) == 0 {
			continue
		}
		dependencies := make([]string, 0, len(b.dependencies[owner]))
		for dependency := range b.dependencies[owner] {
			dependencies = append(dependencies, dependency)
		}
		sort.Strings(dependencies)
		definition.Dependencies[owner] = dependencies
	}
	if len(b.diagnostics) > 0 {
		return nil, b.diagnostics
	}

	canonical, err := json.Marshal(definition)
	if err != nil {
		return nil, fmt.Errorf("encode canonical project definition: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return &Result{
		Hash:       hex.EncodeToString(sum[:]),
		Definition: definition,
	}, nil
}

func (b *builder) compileApplication(key string, source manifest.Application) Application {
	base := "applications." + key
	result := Application{
		Command:     append([]string(nil), source.Command...),
		Environment: make(map[string]Expression, len(source.Environment)),
		Ports:       make(map[string]Port, len(source.Ports)),
		Routes:      make(map[string]Route, len(source.Routes)),
		Volumes:     make(map[string]Volume, len(source.Volumes)),
	}
	if source.Image != "" {
		result.Source = ApplicationSource{Kind: "image", Image: source.Image}
	} else {
		context := b.relativePath(base+".build.context", source.Build.Context, true)
		dockerfile := source.Build.Dockerfile
		if dockerfile == "" {
			dockerfile = "Dockerfile"
		}
		dockerfile = b.relativePath(base+".build.dockerfile", dockerfile, false)
		arguments := make(map[string]Expression, len(source.Build.Arguments))
		for _, name := range mapKeys(source.Build.Arguments) {
			path := base + ".build.arguments." + name
			expression, err := parseExpression(string(source.Build.Arguments[name]), b.document.Project, false)
			if err != nil {
				b.add(path, "%s", err)
				continue
			}
			b.collectVariables(path, expression, false)
			arguments[name] = expression
		}
		result.Source = ApplicationSource{Kind: "build", Build: Build{
			Context:    context,
			Dockerfile: dockerfile,
			Target:     source.Build.Target,
			Arguments:  arguments,
		}}
	}

	owner := "applications." + key
	b.dependencies[owner] = make(map[string]struct{})
	for _, name := range mapKeys(source.Environment) {
		path := base + ".environment." + name
		if !environmentKeyPattern.MatchString(name) {
			b.add(path, "environment variable names must match %s", environmentKeyPattern)
		}
		expression, err := parseExpression(string(source.Environment[name]), b.document.Project, true)
		if err != nil {
			b.add(path, "%s", err)
			continue
		}
		if expressionHasReference(expression) && len(expression.Parts) != 1 {
			b.add(path, "a project variable or service output must occupy the entire environment value")
			continue
		}
		b.collectVariables(path, expression, true)
		for _, part := range expression.Parts {
			if part.Kind == "service_output" {
				b.dependencies[owner][part.Collection+"."+part.Service] = struct{}{}
			}
		}
		result.Environment[name] = expression
	}

	for _, name := range mapKeys(source.Ports) {
		port := source.Ports[name]
		protocol := port.Protocol
		if protocol == "" {
			protocol = "http"
		}
		if !oneOf(protocol, "http", "https", "tcp", "udp") {
			b.add(base+".ports."+name+".protocol", "must be http, https, tcp, or udp")
		}
		result.Ports[name] = Port{Port: port.Port, Protocol: protocol}
	}

	for _, name := range mapKeys(source.Routes) {
		path := base + ".routes." + name
		route := source.Routes[name]
		domain, err := parseExpression(route.Domain, b.document.Project, false)
		if err != nil {
			b.add(path+".domain", "%s", err)
			continue
		}
		b.collectVariables(path+".domain", domain, false)
		routePath := route.Path
		if routePath == "" {
			routePath = "/"
		}
		if !strings.HasPrefix(routePath, "/") {
			b.add(path+".path", "must start with /")
		} else {
			routePath = pathpkg.Clean(routePath)
		}
		tls := route.TLS
		if tls == "" {
			tls = "automatic"
		}
		if !oneOf(tls, "automatic", "disabled") {
			b.add(path+".tls", "must be automatic or disabled")
		}
		target := b.portTarget(path+".port", string(route.Port), source.Ports)
		compiled := Route{Domain: domain, Path: routePath, Port: target, TLS: tls}
		conflictKey := canonicalExpression(domain) + "|" + routePath
		if previous, exists := b.routes[conflictKey]; exists {
			b.add(path, "conflicts with route %s; domain and path pairs must be unique", previous)
		} else {
			b.routes[conflictKey] = path
		}
		result.Routes[name] = compiled
	}

	result.Health.Startup = b.compileProbe(base+".health.startup", source.Health.Startup, source.Ports, 2_000, 30)
	result.Health.Readiness = b.compileProbe(base+".health.readiness", source.Health.Readiness, source.Ports, 10_000, 3)
	result.Health.Liveness = b.compileProbe(base+".health.liveness", source.Health.Liveness, source.Ports, 30_000, 3)
	result.Resources.Requests = b.compileResourceValues(base+".resources.requests", source.Resources.Requests)
	result.Resources.Limits = b.compileResourceValues(base+".resources.limits", source.Resources.Limits)
	b.validateResourceEnvelope(base+".resources", result.Resources)

	minReplicas := source.Scaling.Replicas.Min
	if minReplicas == 0 {
		minReplicas = 1
	}
	maxReplicas := source.Scaling.Replicas.Max
	if maxReplicas == 0 {
		maxReplicas = minReplicas
	}
	if maxReplicas < minReplicas {
		b.add(base+".scaling.replicas.max", "must be greater than or equal to min")
	}
	target := source.Scaling.Autoscaling.CPU.TargetUtilization
	if target < 0 || target > 100 {
		b.add(base+".scaling.autoscaling.cpu.targetUtilization", "must be between 1 and 100")
	}
	if target > 0 && maxReplicas == minReplicas {
		b.add(base+".scaling.autoscaling", "requires replicas.max to be greater than replicas.min")
	}
	if target == 0 && maxReplicas > minReplicas {
		b.add(base+".scaling.autoscaling.cpu.targetUtilization", "is required when replicas.max is greater than replicas.min")
	}
	result.Scaling = Scaling{MinReplicas: minReplicas, MaxReplicas: maxReplicas, CPUTargetUtilization: target}

	spread := source.Placement.Spread
	if spread.Across != "" && !oneOf(spread.Across, "nodes", "zones") {
		b.add(base+".placement.spread.across", "must be nodes or zones")
	}
	if spread.Enforcement != "" && !oneOf(spread.Enforcement, "preferred", "required") {
		b.add(base+".placement.spread.enforcement", "must be preferred or required")
	}
	if spread.Minimum < 0 {
		b.add(base+".placement.spread.minimum", "must not be negative")
	}
	result.Placement = Placement{SpreadAcross: spread.Across, Minimum: spread.Minimum, Enforcement: spread.Enforcement}

	result.Deployment.ReleaseCommand.Command = append([]string(nil), source.Deployment.ReleaseCommand.Command...)
	result.Deployment.ReleaseCommand.TimeoutMillis = b.duration(base+".deployment.releaseCommand.timeout", source.Deployment.ReleaseCommand.Timeout)
	rollout := source.Deployment.Rollout
	strategy := rollout.Strategy
	if strategy == "" {
		strategy = "rolling"
		if len(source.Volumes) > 0 {
			strategy = "recreate"
		}
	}
	if !oneOf(strategy, "rolling", "recreate") {
		b.add(base+".deployment.rollout.strategy", "must be rolling or recreate")
	}
	maxUnavailablePath := base + ".deployment.rollout.maxUnavailable"
	maxSurgePath := base + ".deployment.rollout.maxSurge"
	hasMaxUnavailable := b.document.Has(maxUnavailablePath)
	hasMaxSurge := b.document.Has(maxSurgePath)
	maxUnavailable := rollout.MaxUnavailable
	maxSurge := rollout.MaxSurge
	if maxUnavailable < 0 {
		b.add(maxUnavailablePath, "must not be negative")
	}
	if maxSurge < 0 {
		b.add(maxSurgePath, "must not be negative")
	}
	if strategy == "rolling" && !hasMaxSurge {
		maxSurge = 1
	}
	if strategy == "rolling" && maxUnavailable == 0 && maxSurge == 0 {
		b.add(base+".deployment.rollout", "maxUnavailable and maxSurge cannot both be zero")
	}
	if strategy == "recreate" {
		if hasMaxUnavailable {
			b.add(maxUnavailablePath, "is only valid when strategy is rolling")
		}
		if hasMaxSurge {
			b.add(maxSurgePath, "is only valid when strategy is rolling")
		}
		maxUnavailable = 0
		maxSurge = 0
	}
	if len(source.Volumes) > 0 && strategy == "rolling" {
		b.add(base+".deployment.rollout.strategy", "persistent volumes currently require recreate rollout strategy")
	}
	result.Deployment.Rollout = Rollout{
		Strategy:       strategy,
		MaxUnavailable: maxUnavailable,
		MaxSurge:       maxSurge,
		TimeoutMillis:  b.duration(base+".deployment.rollout.timeout", rollout.Timeout),
	}
	grace := b.duration(base+".shutdown.gracePeriod", source.Shutdown.GracePeriod)
	if grace == 0 {
		grace = 30_000
	}
	result.Shutdown = Shutdown{GracePeriodMillis: grace}

	for _, name := range mapKeys(source.Volumes) {
		volume := source.Volumes[name]
		result.Volumes[name] = Volume{
			MountPath: volume.MountPath,
			SizeBytes: b.bytes(base+".volumes."+name+".size", volume.Size),
		}
		if !strings.HasPrefix(volume.MountPath, "/") {
			b.add(base+".volumes."+name+".mountPath", "must be an absolute container path")
		}
	}
	if len(source.Volumes) > 0 && maxReplicas > 1 {
		b.add(base+".volumes", "persistent volumes currently require a single application replica")
	}
	return result
}

func (b *builder) compileProbe(path string, source manifest.Probe, ports map[string]manifest.Port, defaultInterval int64, defaultFailures int) Probe {
	if !b.document.Has(path) {
		return Probe{}
	}
	target := b.portTarget(path+".http.port", string(source.HTTP.Port), ports)
	if source.HTTP.Port == "" {
		b.add(path+".http.port", "is required")
	}
	httpPath := source.HTTP.Path
	if httpPath == "" || !strings.HasPrefix(httpPath, "/") {
		b.add(path+".http.path", "must start with /")
	}
	interval := b.duration(path+".interval", source.Interval)
	if interval == 0 {
		interval = defaultInterval
	}
	timeout := b.duration(path+".timeout", source.Timeout)
	if timeout == 0 {
		timeout = 2_000
	}
	failures := source.FailureThreshold
	if failures == 0 {
		failures = defaultFailures
	}
	return Probe{
		HTTP:             HTTPProbe{Port: target, Path: httpPath},
		IntervalMillis:   interval,
		TimeoutMillis:    timeout,
		FailureThreshold: failures,
	}
}

func (b *builder) compileResourceValues(path string, source manifest.ResourceValues) ResourceValues {
	return ResourceValues{
		MilliCPU:              b.cpu(path+".cpu", source.CPU),
		MemoryBytes:           b.bytes(path+".memory", source.Memory),
		TemporaryStorageBytes: b.bytes(path+".temporaryStorage", source.TemporaryStorage),
	}
}

func (b *builder) validateResourceEnvelope(path string, resources Resources) {
	if resources.Requests.MilliCPU > 0 && resources.Limits.MilliCPU > 0 && resources.Requests.MilliCPU > resources.Limits.MilliCPU {
		b.add(path+".requests.cpu", "must not exceed the CPU limit")
	}
	if resources.Requests.MemoryBytes > 0 && resources.Limits.MemoryBytes > 0 && resources.Requests.MemoryBytes > resources.Limits.MemoryBytes {
		b.add(path+".requests.memory", "must not exceed the memory limit")
	}
	if resources.Requests.TemporaryStorageBytes > 0 && resources.Limits.TemporaryStorageBytes > 0 && resources.Requests.TemporaryStorageBytes > resources.Limits.TemporaryStorageBytes {
		b.add(path+".requests.temporaryStorage", "must not exceed the temporary storage limit")
	}
}

func (b *builder) compileDatabase(key string, source manifest.Database) DatabaseClaim {
	base := "databases." + key
	if source.Engine != "postgres" {
		b.add(base+".engine", "unsupported database engine %q; currently only postgres is available", source.Engine)
	}
	isolation := source.Isolation
	if isolation == "" {
		isolation = "shared"
	}
	if !oneOf(isolation, "shared", "project", "dedicated") {
		b.add(base+".isolation", "must be shared, project, or dedicated")
	}
	availability := source.Availability
	if availability == "" {
		availability = "single"
	}
	if !oneOf(availability, "single", "asynchronous", "synchronous") {
		b.add(base+".availability", "must be single, asynchronous, or synchronous")
	}
	extensions := append([]string(nil), source.Extensions...)
	sort.Strings(extensions)
	recoveryMillis := b.duration(base+".recovery.pointInTime", source.Recovery.PointInTime)
	return DatabaseClaim{
		Engine:                 source.Engine,
		Version:                string(source.Version),
		Isolation:              isolation,
		Availability:           availability,
		StorageBytes:           b.bytes(base+".storage.size", source.Storage.Size),
		Extensions:             extensions,
		PointInTimeRecoverySec: recoveryMillis / 1000,
	}
}

func (b *builder) compileBucket(key string, source manifest.Bucket) BucketClaim {
	base := "buckets." + key
	visibility := source.Visibility
	if visibility == "" {
		visibility = "private"
	}
	if !oneOf(visibility, "private", "public-read") {
		b.add(base+".visibility", "must be private or public-read")
	}
	versioning := source.Versioning
	if versioning == "" {
		versioning = "disabled"
	}
	if !oneOf(versioning, "enabled", "disabled") {
		b.add(base+".versioning", "must be enabled or disabled")
	}
	if source.Quotas.Objects < 0 {
		b.add(base+".quotas.objects", "must not be negative")
	}
	return BucketClaim{
		Visibility:                         visibility,
		StorageQuotaBytes:                  b.bytes(base+".quotas.storage", source.Quotas.Storage),
		ObjectQuota:                        source.Quotas.Objects,
		MaxObjectSizeBytes:                 b.bytes(base+".quotas.maxObjectSize", source.Quotas.MaxObjectSize),
		Versioning:                         versioning,
		AbortIncompleteUploadsAfterSeconds: b.duration(base+".lifecycle.abortIncompleteUploadsAfter", source.Lifecycle.AbortIncompleteUploadsAfter) / 1000,
		ExpireNoncurrentVersionsAfterSec:   b.duration(base+".lifecycle.expireNoncurrentVersionsAfter", source.Lifecycle.ExpireNoncurrentVersionsAfter) / 1000,
	}
}

func (b *builder) compileBackup(key string, source manifest.Backup) Backup {
	base := "backups." + key
	if len(strings.Fields(source.Schedule)) != 5 {
		b.add(base+".schedule", "must be a five-field cron expression")
	}
	retention := b.duration(base+".retention", source.Retention)
	databases := append([]string(nil), source.Include.Databases.Keys...)
	buckets := append([]string(nil), source.Include.Buckets.Keys...)
	volumes := append([]string(nil), source.Include.Volumes.Keys...)
	sort.Strings(databases)
	sort.Strings(buckets)
	sort.Strings(volumes)
	return Backup{
		Schedule:         source.Schedule,
		RetentionSeconds: retention / 1000,
		Include: Selection{
			AllDatabases: source.Include.Databases.All,
			Databases:    databases,
			AllBuckets:   source.Include.Buckets.All,
			Buckets:      buckets,
			AllVolumes:   source.Include.Volumes.All,
			Volumes:      volumes,
		},
	}
}

func (b *builder) collectVariables(path string, expression Expression, secretAllowed bool) {
	for _, part := range expression.Parts {
		if part.Kind != "project_variable" {
			continue
		}
		declaration, declared := b.document.Project.Values[part.Name]
		if declared && declaration.Secret {
			if part.HasDefault {
				b.add(path, "secret project value %s cannot carry an inline default", part.Name)
			}
			if !secretAllowed {
				b.add(path, "secret project value %s may only be used in application environment variables", part.Name)
			}
		}
		requirement, exists := b.variables[part.Name]
		if !exists {
			requirement = VariableRequirement{
				Name:       part.Name,
				Required:   !part.HasDefault,
				Default:    part.Default,
				HasDefault: part.HasDefault,
			}
			if declared {
				requirement.Secret = declaration.Secret
				requirement.Description = declaration.Description
			}
			b.variables[part.Name] = requirement
			b.variablePath[part.Name] = path
			continue
		}
		if requirement.HasDefault && part.HasDefault && requirement.Default != part.Default {
			b.add(path, "project variable %s has conflicting defaults %q and %q", part.Name, requirement.Default, part.Default)
		}
		if !part.HasDefault {
			requirement.Required = true
		}
		b.variables[part.Name] = requirement
	}
}

func (b *builder) portTarget(path, value string, ports map[string]manifest.Port) PortTarget {
	if number, err := strconv.Atoi(value); err == nil {
		if number < 1 || number > 65535 {
			b.add(path, "must be between 1 and 65535")
		}
		return PortTarget{Number: number}
	}
	if _, ok := ports[value]; !ok {
		b.add(path, "references unknown application port %q", value)
	}
	return PortTarget{Name: value}
}

func (b *builder) relativePath(path, value string, allowDot bool) string {
	if filepath.IsAbs(value) {
		b.add(path, "must be relative to the project root")
		return value
	}
	cleaned := filepath.Clean(value)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		b.add(path, "must not escape the project root")
	}
	if !allowDot && cleaned == "." {
		b.add(path, "must name a file relative to the build context")
	}
	return filepath.ToSlash(cleaned)
}

func (b *builder) bytes(path string, value manifest.Text) int64 {
	result, err := parseBytes(string(value))
	if err != nil {
		b.add(path, "%s", err)
	}
	return result
}

func (b *builder) cpu(path string, value manifest.Text) int64 {
	result, err := parseCPU(string(value))
	if err != nil {
		b.add(path, "%s", err)
	}
	return result
}

func (b *builder) duration(path string, value manifest.Text) int64 {
	result, err := parseDuration(string(value))
	if err != nil {
		b.add(path, "%s", err)
	}
	return result
}

func (b *builder) add(path, format string, args ...any) {
	b.diagnostics = append(b.diagnostics, b.document.Diagnostic(path, fmt.Sprintf(format, args...)))
}

func mapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func oneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

func canonicalExpression(expression Expression) string {
	data, _ := json.Marshal(expression)
	return string(data)
}

func ValidateEnvironment(result *Result, values map[string]string) error {
	var missing []string
	known := make(map[string]struct{}, len(result.Definition.RequiredVariables))
	for _, requirement := range result.Definition.RequiredVariables {
		known[requirement.Name] = struct{}{}
		if value, ok := values[requirement.Name]; (!ok || value == "") && requirement.Required {
			missing = append(missing, requirement.Name)
		}
	}
	var unknown []string
	for name := range values {
		if _, ok := known[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(missing) == 0 && len(unknown) == 0 {
		return nil
	}
	sort.Strings(missing)
	sort.Strings(unknown)
	var problems []string
	if len(missing) > 0 {
		problems = append(problems, "missing project variables: "+strings.Join(missing, ", "))
	}
	if len(unknown) > 0 {
		problems = append(problems, "unknown project variables: "+strings.Join(unknown, ", "))
	}
	return fmt.Errorf("%s", strings.Join(problems, "; "))
}
