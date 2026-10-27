package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/build"
	"github.com/Hinkolas/skali/internal/checkout"
	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/utils"
	"github.com/Hinkolas/skali/internal/values"
)

// deployOptions are the shared knobs of skali plan, deploy, and dev.
type deployOptions struct {
	// Remote names the remote to target explicitly, bypassing both the
	// checkout binding and the current remote. dev sets it to the local
	// remote so the local platform never has to be the current remote.
	Remote      string
	Environment string
	Manifest    string
	EnvFile     string
	BuildMode   string
	// From promotes another environment's active revision instead of the
	// local checkout; no manifest is read and nothing builds.
	From             string
	Yes              bool
	AllowDestructive bool
	Detach           bool
	// Attach chooses the key bindings while following the run; the dev
	// session keeps Ctrl-C for itself.
	Attach attachMode
	// Force deploys even when the environment is up to date; application
	// workloads restart at promotion. Data is never touched.
	Force bool
	// BypassProtection deploys into a promote-only environment anyway. The
	// server consumes it only when the policy would refuse, then requires
	// environment admin and a recent login, and records it on the run.
	// Force keeps its own meaning (restart even when nothing changed).
	BypassProtection bool
	// Rebuild ignores artifact reuse and disables build caches so moved
	// upstream tags and refreshed base images are picked up.
	Rebuild bool
	// PruneValues removes the stored values the manifest no longer
	// references as part of this deployment (plan rows instead of the
	// orphaned warning).
	PruneValues bool
	// Platform overrides the build platform(s); empty follows the
	// server-reported cluster architecture.
	Platform string
	// AutoEnvFile takes the project root's lone env file without asking
	// (bare dev); with several the picker runs like deploy's.
	AutoEnvFile bool
	// PickEnvFile re-opens the env file picker although a choice is
	// remembered for the environment, and remembers the new answer.
	PickEnvFile bool
	// CreateMissing provisions the project and environment through the API
	// when absent (local dev); remote deploys create only interactively,
	// behind explicit confirmation.
	CreateMissing bool
	// UseBinding reads and writes the .skali/ checkout binding; set by
	// plan and deploy. dev targets the local remote through Remote and
	// never touches the binding (the env file memory beside it is shared).
	UseBinding bool
	// OnDeploymentOpened and OnDeploymentClosed observe the artifact
	// window so a signaled dev session can fail an interrupted window on a
	// fresh context (pause-on-exit); both optional.
	OnDeploymentOpened func(deploymentID string)
	OnDeploymentClosed func()
	// LocalApplications declares dev-block applications this session runs
	// on the host: they are not built and the server intercepts their
	// Service. Only bare skali dev sets it; plan, deploy, promote, and dev
	// --preview leave it nil, so those flows build dev-block apps exactly
	// like any other.
	LocalApplications map[string]client.LocalApplication
	// SkipReadySummary leaves the route summary under the ready line to the
	// caller; dev prints its own with the dashboard and host dev processes.
	SkipReadySummary bool
}

// project bundles everything the flow knows about the local checkout.
type localProject struct {
	Path   string // manifest path
	Root   string // project root (manifest directory)
	Source []byte
	Result *compiler.Result
	// Document keeps the parsed manifest: the client-only authoring
	// surface (dev blocks, named commands) is deliberately absent from the
	// compiled Result and is read from here.
	Document *manifest.Document
}

// devApplications returns the manifest's dev-block applications by key.
// Presence is read from the document (an empty dev block is still a dev
// block, caught by validation before it gets here).
func devApplications(project *localProject) map[string]manifest.Dev {
	devs := map[string]manifest.Dev{}
	for key, application := range project.Document.Project.Applications {
		if project.Document.Has("applications." + key + ".dev") {
			devs[key] = application.Dev
		}
	}
	return devs
}

func loadLocalProject(explicit string, outputs ...io.Writer) (*localProject, error) {
	return readLocalProject(explicit, reviewOutput(outputs))
}

// A nil review output selects metadata-only loading, with no local state access.
func readLocalProject(explicit string, review io.Writer) (*localProject, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	path, err := manifest.Discover(explicit, workingDirectory)
	if err != nil {
		return nil, err
	}
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	document, err := manifest.Parse(source, path)
	if err != nil {
		return nil, err
	}
	var result *compiler.Result
	if review == nil {
		result, err = compiler.Compile(document)
	} else {
		result, err = compileReviewed(document, review)
	}
	if err != nil {
		return nil, err
	}
	return &localProject{
		Path:     path,
		Root:     filepath.Dir(path),
		Source:   source,
		Result:   result,
		Document: document,
	}, nil
}

// findProject lists the installation's projects once and returns the
// named one, or nil when absent.
func findProject(ctx context.Context, api *client.Client, name string) (*client.Project, error) {
	projects, err := api.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	for i := range projects {
		if projects[i].Name == name {
			return &projects[i], nil
		}
	}
	return nil, nil
}

// findEnvironment returns the named environment among the given, or nil.
func findEnvironment(environments []client.Environment, name string) *client.Environment {
	for i := range environments {
		if environments[i].Name == name || slices.Contains(environments[i].PreviousNames, name) {
			return &environments[i]
		}
	}
	return nil
}

// resolveEnvironmentIDs finds the project and environment on the target
// installation; both must exist.
func resolveEnvironmentIDs(ctx context.Context, api *client.Client, projectName, environmentName string) (projectID, environmentID string, err error) {
	project, err := findProject(ctx, api, projectName)
	if err != nil {
		return "", "", err
	}
	if project == nil {
		return "", "", fmt.Errorf("project %s does not exist on this installation", projectName)
	}
	environments, err := api.ListEnvironments(ctx, project.ID)
	if err != nil {
		return "", "", err
	}
	environment := findEnvironment(environments, environmentName)
	if environment == nil {
		return "", "", fmt.Errorf("environment %s does not exist in project %s", environmentName, projectName)
	}
	return project.ID, environment.ID, nil
}

// sameMaster reports whether two master URLs identify the same
// installation: folded scheme and host, identical path after trailing
// slashes are trimmed. Scheme and port differences are distinct
// installations by design.
func sameMaster(a, b string) bool {
	trimmedA := strings.TrimRight(strings.TrimSpace(a), "/")
	trimmedB := strings.TrimRight(strings.TrimSpace(b), "/")
	parsedA, errA := url.Parse(trimmedA)
	parsedB, errB := url.Parse(trimmedB)
	if errA != nil || errB != nil {
		return strings.EqualFold(trimmedA, trimmedB)
	}
	return strings.EqualFold(parsedA.Scheme, parsedB.Scheme) &&
		strings.EqualFold(parsedA.Host, parsedB.Host) &&
		parsedA.Path == parsedB.Path
}

// remoteByName resolves an explicit --remote value against the config. The
// dev-owned local remote gets its own hint: it is hidden from listings, so
// "run skali remote list" would point nowhere.
func remoteByName(cfg *cliconfig.Config, name string) (*cliconfig.Remote, error) {
	if remote := cfg.Remotes[name]; remote != nil {
		return remote, nil
	}
	if name == localRemoteName {
		return nil, errors.New("the local platform is not set up; run `skali dev` first")
	}
	return nil, fmt.Errorf("remote %q does not exist; run `skali remote list`", name)
}

// lookupRemoteByMaster finds the remote whose master URL matches, scanning
// names in sorted order so duplicates resolve deterministically.
func lookupRemoteByMaster(cfg *cliconfig.Config, master string) (string, *cliconfig.Remote, bool) {
	names := make([]string, 0, len(cfg.Remotes))
	for name := range cfg.Remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if remote := cfg.Remotes[name]; remote != nil && sameMaster(remote.Master, master) {
			return name, remote, true
		}
	}
	return "", nil, false
}

// roleRank orders the access ladder for client-side hints. The server is
// the authority; these checks only refuse before any work is done.
var roleRank = map[string]int{"none": 0, "read": 1, "deploy": 2, "maintain": 3, "admin": 4}

// roleAtLeast reports whether role reaches min. An empty role (the server
// did not report access) passes: the server decides.
func roleAtLeast(role, min string) bool {
	if role == "" {
		return true
	}
	rank, known := roleRank[role]
	return !known || rank >= roleRank[min]
}

// checkDeployAccess refuses a deploy the server would refuse anyway, before
// the definition is submitted: below deploy nothing can be deployed.
func checkDeployAccess(access, environment string) error {
	if roleAtLeast(access, "deploy") {
		return nil
	}
	return fmt.Errorf("deploy on environment %s required (your role: %s)", environment, access)
}

// valuesStagingAllowed decides whether this invocation may stage values.
// Staging needs maintain on the environment; an explicit --env-file or
// --prune-values below that is refused up front, while a merely discovered
// env file is skipped so a deploy-role user still deploys code with the
// stored values.
func valuesStagingAllowed(access, environment string, opts *deployOptions) (bool, error) {
	if roleAtLeast(access, "maintain") {
		return true, nil
	}
	if opts.EnvFile != "" {
		return false, fmt.Errorf("staging values needs maintain on environment %s (your role: %s); "+
			"drop --env-file to deploy with the stored values", environment, access)
	}
	if opts.PruneValues {
		return false, fmt.Errorf("pruning values needs maintain on environment %s (your role: %s); "+
			"drop --prune-values to deploy with the stored values", environment, access)
	}
	return false, nil
}

// checkDeployPolicy refuses what the server's protection policy would
// refuse anyway, before the definition is submitted or a promotion source
// is read: under promote-only a direct deploy, or a promotion from a
// source outside the allowed list, needs the explicit bypass, and the
// bypass needs admin on the environment. Returns whether the bypass will
// be consumed (the server records it and the flow prints it). Nil settings
// (locked environment, older server) disable the check; the server still
// decides. source is the promotion source's name, empty for a direct
// deploy.
func checkDeployPolicy(settings *client.EnvironmentSettings, access, environment, source string, opts *deployOptions) (bool, error) {
	if settings == nil || settings.DeployPolicy != "promote-only" {
		return false, nil
	}
	if source != "" && (len(settings.PromoteFrom) == 0 || slices.Contains(settings.PromoteFrom, source)) {
		return false, nil
	}
	if !opts.BypassProtection {
		if source != "" {
			return false, fmt.Errorf("environment %s accepts promotions from %s only, not from %s; "+
				"environment admins may pass --bypass-protection",
				environment, strings.Join(settings.PromoteFrom, " or "), source)
		}
		from := "<environment>"
		if len(settings.PromoteFrom) > 0 {
			from = strings.Join(settings.PromoteFrom, " or ")
		}
		return false, fmt.Errorf("environment %s is promote-only: promote with skali deploy --from %s --environment %s; "+
			"environment admins may pass --bypass-protection", environment, from, environment)
	}
	if !roleAtLeast(access, "admin") {
		return false, fmt.Errorf("bypassing protection needs admin on environment %s (your role: %s)", environment, access)
	}
	return true, nil
}

// printProtectionBypassed follows the plan when the server consumed the
// bypass: the deploy goes into a promote-only environment on purpose and
// the run says so.
func printProtectionBypassed(out io.Writer, environment string) {
	style := clirender.StyleFor(out)
	fmt.Fprintf(out, "%s   %s\n", style.Dim("protection"),
		style.Yellow("bypassed: environment "+environment+" is promote-only (recorded on the run)"))
}

// valuesSource says where a deployment's values come from; the header
// names it so a remembered choice stays visible without a prompt.
type valuesSource int

const (
	// valuesStored is the environment's stored values with nothing
	// remembered: no env file to choose from, or a non-interactive run.
	valuesStored valuesSource = iota
	// valuesStoredRemembered is the stored values by remembered choice.
	valuesStoredRemembered
	// valuesFlag is an explicit --env-file; one-shot, the memory is left
	// alone like --environment leaves the binding alone.
	valuesFlag
	// valuesRemembered is the env file remembered for the environment.
	valuesRemembered
	// valuesPicked is the env file picked interactively just now, and
	// remembered from here on.
	valuesPicked
	// valuesDiscovered is the lone env file of the project root, taken
	// without asking (dev) and remembered.
	valuesDiscovered
)

// selectedValues is what selectValues decided: the file (nil for the
// stored values), where it came from, and the fingerprint of the value
// set the last deployment from this checkout carried to the environment
// (empty when none was recorded), for the changed/unchanged note.
type selectedValues struct {
	file         *values.File
	source       valuesSource
	lastDeployed string
}

// selectValues decides the value source for opts.Environment. In order: an
// explicit --env-file; the choice remembered for the environment in
// .skali/env-files.yaml (unless --pick-env-file); nothing when the project
// root has no env files; the lone env file under dev (AutoEnvFile); the
// interactive picker, whose answer is remembered, including "stored
// values"; and the stored values when nothing can be asked. A remembered
// file that disappeared re-opens the picker interactively and is an error
// otherwise: the stored values are never substituted silently. interactive
// is whether a picker may be shown (dev's implied --yes only skips
// confirmations, so dev passes cliprompt.Interactive() alone).
func selectValues(out io.Writer, in *bufio.Reader, project *localProject, opts *deployOptions, interactive bool) (*selectedValues, error) {
	if opts.EnvFile != "" && opts.PickEnvFile {
		return nil, errors.New("--env-file and --pick-env-file are mutually exclusive")
	}
	if opts.PickEnvFile && !interactive {
		return nil, errors.New("--pick-env-file needs an interactive terminal; pass --env-file instead")
	}
	memory, err := checkout.LoadEnvFiles(project.Root)
	if err != nil {
		return nil, err
	}
	remembered, hasMemory := memory[opts.Environment]
	selected := &selectedValues{source: valuesStored, lastDeployed: remembered.Fingerprint}
	if opts.EnvFile != "" {
		if selected.file, err = values.ParseFile(opts.EnvFile); err != nil {
			return nil, err
		}
		selected.source = valuesFlag
		return selected, nil
	}
	style := clirender.StyleFor(out)
	if hasMemory && !opts.PickEnvFile {
		if remembered.Stored {
			selected.source = valuesStoredRemembered
			return selected, nil
		}
		path := remembered.Resolve(project.Root)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			if selected.file, err = values.ParseFile(path); err != nil {
				return nil, err
			}
			selected.source = valuesRemembered
			return selected, nil
		}
		if !interactive {
			return nil, fmt.Errorf("remembered env file %s for environment %s is missing; "+
				"pass --env-file, or run interactively to pick again", remembered.File, opts.Environment)
		}
		fmt.Fprintln(out, style.Dim(fmt.Sprintf("remembered env file %s is gone; pick again", remembered.File)))
	}
	candidates := discoverEnvFiles(project.Root)
	if len(candidates) == 0 {
		return selected, nil
	}
	path := ""
	if opts.AutoEnvFile && len(candidates) == 1 && !opts.PickEnvFile {
		path, selected.source = candidates[0], valuesDiscovered
	} else {
		if !interactive {
			return selected, nil
		}
		if path, err = chooseEnvFile(out, in, project, opts.Environment, candidates, remembered); err != nil {
			return nil, err
		}
		selected.source = valuesPicked
	}
	choice := checkout.EnvFileChoice{File: path, Stored: path == ""}
	if err := checkout.SaveEnvFileChoice(project.Root, opts.Environment, choice); err != nil {
		return nil, err
	}
	if path == "" {
		selected.source = valuesStoredRemembered
		return selected, nil
	}
	if selected.file, err = values.ParseFile(path); err != nil {
		return nil, err
	}
	return selected, nil
}

// templateSuffixes mark env files that are committed examples rather than
// values to deploy; discovery skips them, --env-file still accepts them.
var templateSuffixes = []string{".example", ".sample", ".template"}

// discoverEnvFiles lists the project root's .env and .env.* files, .env
// first, for the interactive selection; templates are left out.
func discoverEnvFiles(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name != ".env" && !strings.HasPrefix(name, ".env.") {
			continue
		}
		if slices.ContainsFunc(templateSuffixes, func(suffix string) bool { return strings.HasSuffix(name, suffix) }) {
			continue
		}
		files = append(files, filepath.Join(root, name))
	}
	sort.Strings(files)
	return files
}

// chooseEnvFile asks which of the candidates deploys to environment, or
// the stored values (empty result). The cursor starts on the file last
// used for the environment, else .env.<environment>, else .env; each
// candidate says whether it matches the environment, was last used, and,
// once a deployment recorded a fingerprint, whether its values changed
// since.
func chooseEnvFile(out io.Writer, in *bufio.Reader, project *localProject, environment string,
	candidates []string, remembered checkout.EnvFileChoice) (string, error) {
	options := make([]cliprompt.Option, 0, len(candidates)+1)
	options = append(options, cliprompt.Option{Label: "stored values", Value: ""})
	lastUsed := remembered.Resolve(project.Root)
	defaultValue := ""
	for _, rank := range []string{lastUsed, filepath.Join(project.Root, ".env."+environment), filepath.Join(project.Root, ".env")} {
		if rank != "" && slices.Contains(candidates, rank) {
			defaultValue = rank
			break
		}
	}
	for _, candidate := range candidates {
		var hints []string
		if filepath.Base(candidate) == ".env."+environment {
			hints = append(hints, "matches environment")
		}
		if candidate == lastUsed {
			hints = append(hints, "last used")
		}
		if remembered.Fingerprint != "" {
			hints = append(hints, describeValuesChange(project, candidate, remembered.Fingerprint))
		}
		options = append(options, cliprompt.Option{
			Label:       filepath.Base(candidate),
			Description: strings.Join(hints, ", "),
			Value:       candidate,
		})
	}
	return promptSession(out, in).Select(context.Background(), cliprompt.SelectOptions{
		Title:        fmt.Sprintf("Values for %s?", environment),
		Description:  "Remembered for this checkout; --pick-env-file asks again.",
		Options:      options,
		DefaultValue: defaultValue,
	})
}

// describeValuesChange compares an env file's deployable value set with
// the fingerprint of the environment's last deployment from this checkout.
func describeValuesChange(project *localProject, path, lastDeployed string) string {
	file, err := values.ParseFile(path)
	if err != nil {
		return "unreadable: " + err.Error()
	}
	if deployableFingerprint(project, file) == lastDeployed {
		return "unchanged since last deploy"
	}
	return "changed since last deploy"
}

// deployableFingerprint hashes the values of file a deployment would
// carry: the set conformed to the definition's variables, so orphaned keys
// do not count. Without a compiled definition (tests) the whole file does.
func deployableFingerprint(project *localProject, file *values.File) string {
	if project.Result == nil {
		return values.Fingerprint(file.Values)
	}
	kept, _, _ := values.Conform(project.Result.Definition.RequiredVariables, file.Values)
	return values.Fingerprint(kept)
}

// describeValuesSource is the header note for the values row: the source,
// the change since the last deployment when known, and the staging detail.
func describeValuesSource(selected *selectedValues, project *localProject, staging string) string {
	var parts []string
	switch selected.source {
	case valuesStoredRemembered, valuesRemembered:
		parts = append(parts, "remembered")
	case valuesFlag:
		parts = append(parts, "--env-file")
	case valuesPicked:
		parts = append(parts, "picked")
	case valuesDiscovered:
		parts = append(parts, "discovered")
	}
	if selected.file != nil && selected.lastDeployed != "" {
		if deployableFingerprint(project, selected.file) == selected.lastDeployed {
			parts = append(parts, "unchanged since last deploy")
		} else {
			parts = append(parts, "changed since last deploy")
		}
	}
	note := strings.Join(parts, ", ")
	if staging != "" {
		if note != "" {
			note += "; "
		}
		note += staging
	}
	return note
}

// displayPath shows an env file relative to the project root when it lives
// under it, absolute otherwise.
func displayPath(root, path string) string {
	if relative, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(relative, "..") {
		return relative
	}
	return path
}

// chooseEnvironment asks for one of the project's environments; a single
// environment selects itself. The caller guarantees at least one.
func chooseEnvironment(out io.Writer, in *bufio.Reader, environments []client.Environment) (string, error) {
	if len(environments) == 1 {
		return environments[0].Name, nil
	}
	options := make([]cliprompt.Option, 0, len(environments))
	for _, environment := range environments {
		options = append(options, cliprompt.Option{
			Label: environment.Name,
			Value: environment.Name,
		})
	}
	return promptSession(out, in).Select(context.Background(), cliprompt.SelectOptions{
		Title:   "Which environment should Skali use?",
		Options: options,
	})
}

// resolveAppPlatforms picks the platform each build-sourced application
// targets. Per application, in order: an explicit --platform override wins
// (validated against the application's buildable platforms); otherwise the
// candidates are the manifest's declared platforms intersected with the
// platforms the server observed on the cluster's application nodes (either
// side unknown keeps the other); a cluster platform preference picks the
// first preferred candidate for a single-arch build; without a preference
// every candidate joins into one comma-separated multi-platform build; and
// when nothing is known the host architecture stands in (older servers,
// observation not yet synced). Declared platforms disjoint from the
// cluster's are a hard error, for image-sourced applications too: the
// workload could not schedule anywhere.
func resolveAppPlatforms(out io.Writer, status *client.EnvironmentStatus, override string,
	definition compiler.ProjectDefinition, skip map[string]client.LocalApplication) (map[string]string, error) {

	local := "linux/" + runtime.GOARCH
	var observed, preference []string
	if status != nil {
		observed = status.Platforms
		preference = status.PlatformPreference
	}
	overridePlatform := ""
	if override != "" {
		overridePlatform = canonicalPlatforms(strings.Split(override, ","))
	}

	platforms := make(map[string]string)
	reasons := make(map[string]string)
	for _, key := range utils.SortedKeys(definition.Applications) {
		if _, ok := skip[key]; ok {
			continue
		}
		source := definition.Applications[key].Source
		declared := source.Platforms
		candidates := platformCandidates(declared, observed)
		if len(declared) > 0 && len(observed) > 0 && len(candidates) == 0 {
			return nil, fmt.Errorf("application %s supports only %s but the cluster's application nodes run %s",
				key, strings.Join(declared, ", "), strings.Join(observed, ", "))
		}
		if source.Kind != "build" {
			continue
		}
		switch {
		case overridePlatform != "":
			if len(candidates) > 0 && !platformsIntersect(strings.Split(overridePlatform, ","), candidates) {
				return nil, fmt.Errorf("application %s cannot be built for %s; its buildable platforms are %s",
					key, overridePlatform, strings.Join(candidates, ", "))
			}
			platforms[key], reasons[key] = overridePlatform, "override"
		case len(candidates) == 0:
			platforms[key] = local
			reasons[key] = "local architecture; the server did not report cluster platforms"
		default:
			if preferred, ok := firstPreferredPlatform(preference, candidates); ok {
				platforms[key], reasons[key] = preferred, "cluster preference"
			} else if len(declared) > 0 && len(candidates) < len(observed) {
				platforms[key], reasons[key] = canonicalPlatforms(candidates), "declared platforms"
			} else {
				platforms[key], reasons[key] = canonicalPlatforms(candidates), "cluster architecture"
			}
		}
	}
	printAppPlatforms(out, platforms, reasons, local)
	return platforms, nil
}

// printAppPlatforms reports the chosen build platforms, staying quiet for
// the common case of every application building for the local architecture
// as a matter of course. One shared line covers uniform resolutions; mixed
// resolutions print per application.
func printAppPlatforms(out io.Writer, platforms, reasons map[string]string, local string) {
	style := clirender.StyleFor(out)
	uniform := true
	first := ""
	for _, key := range utils.SortedKeys(platforms) {
		if first == "" {
			first = platforms[key] + "\x00" + reasons[key]
		} else if first != platforms[key]+"\x00"+reasons[key] {
			uniform = false
			break
		}
	}
	if len(platforms) == 0 {
		return
	}
	if uniform {
		key := utils.SortedKeys(platforms)[0]
		platform, reason := platforms[key], reasons[key]
		if platform == local && (reason == "cluster architecture" || reason == "cluster preference") {
			return
		}
		fmt.Fprintf(out, "%s     %s %s\n", style.Dim("platform"), platform, style.Dim("("+reason+")"))
		return
	}
	for _, key := range utils.SortedKeys(platforms) {
		fmt.Fprintf(out, "%s     %s %s\n", style.Dim("platform"), platforms[key],
			style.Dim("("+key+": "+reasons[key]+")"))
	}
}

// platformCandidates intersects an application's declared platforms with
// the observed cluster platforms. Either side being empty means unknown
// and keeps the other side unchanged, mirroring the server's guard.
func platformCandidates(declared, observed []string) []string {
	if len(declared) == 0 {
		return observed
	}
	if len(observed) == 0 {
		return declared
	}
	var common []string
	for _, platform := range declared {
		if slices.Contains(observed, platform) {
			common = append(common, platform)
		}
	}
	return common
}

// platformsIntersect reports whether any submitted platform is in the
// candidate set.
func platformsIntersect(submitted, candidates []string) bool {
	for _, platform := range submitted {
		if slices.Contains(candidates, strings.TrimSpace(platform)) {
			return true
		}
	}
	return false
}

// firstPreferredPlatform picks the first entry of the ordered cluster
// preference that the candidate set contains.
func firstPreferredPlatform(preference, candidates []string) (string, bool) {
	for _, platform := range preference {
		if slices.Contains(candidates, platform) {
			return platform, true
		}
	}
	return "", false
}

// canonicalPlatforms trims, dedupes, and sorts so that equal platform sets
// produce equal strings and therefore equal input hashes.
func canonicalPlatforms(platforms []string) string {
	seen := make(map[string]struct{}, len(platforms))
	cleaned := make([]string, 0, len(platforms))
	for _, platform := range platforms {
		platform = strings.TrimSpace(platform)
		if platform == "" {
			continue
		}
		if _, ok := seen[platform]; ok {
			continue
		}
		seen[platform] = struct{}{}
		cleaned = append(cleaned, platform)
	}
	sort.Strings(cleaned)
	return strings.Join(cleaned, ",")
}

// buildInputs computes the per-application hashes: the dedup key the
// server decides reuse with. Selected env files never enter the context.
// Applications in the skip set (host-run intercepts) hash nothing. Each
// application hashes its own resolved platform.
func buildInputs(project *localProject, excludeFiles []string, platforms map[string]string,
	skip map[string]client.LocalApplication) (map[string]client.BuildInput, map[string]*build.Context, error) {
	inputs := make(map[string]client.BuildInput)
	contexts := make(map[string]*build.Context)
	for key, application := range project.Result.Definition.Applications {
		if application.Source.Kind != "build" {
			continue
		}
		if _, ok := skip[key]; ok {
			continue
		}
		spec := application.Source.Build
		collected, err := build.Collect(project.Root, spec.Context, build.CollectOptions{ExcludeFiles: excludeFiles})
		if err != nil {
			return nil, nil, fmt.Errorf("collect build context for %s: %w", key, err)
		}
		dockerfile, err := os.ReadFile(filepath.Join(project.Root, spec.Dockerfile))
		if err != nil {
			return nil, nil, fmt.Errorf("read Dockerfile for %s: %w", key, err)
		}
		configHash := build.ConfigHash(dockerfile, spec.Target, spec.Arguments)
		platform := platforms[key]
		inputs[key] = client.BuildInput{
			InputHash:  build.InputHash(collected.TreeHash, configHash, platform),
			ConfigHash: configHash,
			Platform:   platform,
		}
		contexts[key] = collected
	}
	return inputs, contexts, nil
}

func printPlan(out io.Writer, plan *client.PlanDocument, actions []client.ArtifactAction, activeChecksum string) {
	style := clirender.StyleFor(out)
	if activeChecksum != "" {
		fmt.Fprintf(out, "\nplan against active revision %s\n", utils.ShortChecksum(activeChecksum))
	} else {
		fmt.Fprintf(out, "\nplan for the initial deployment\n")
	}
	rebuilt := make(map[string]string, len(actions))
	for _, action := range actions {
		rebuilt[action.Application] = action.Action
	}
	for _, change := range plan.Changes {
		var reasons []string
		if change.Detail != "" {
			reasons = strings.Split(change.Detail, "; ")
		}
		if action, ok := rebuilt[strings.TrimPrefix(change.Service, "applications.")]; ok && change.Action != "remove" {
			switch action {
			case "build":
				reasons = append(reasons, "artifact will be rebuilt")
			case "import":
				reasons = append(reasons, "image will be imported")
			case "reuse":
				reasons = append(reasons, "artifact reused")
			}
		}
		first, rest := "", []string(nil)
		if len(reasons) > 0 {
			first, rest = reasons[0], reasons[1:]
		}
		if change.Destructive {
			first = style.BoldRed("DESTRUCTIVE:") + " " + first
		}
		fmt.Fprintf(out, "  %s %-24s %s\n", actionColor(style, change.Action), change.Service, first)
		for _, reason := range rest {
			fmt.Fprintf(out, "  %7s %-24s %s\n", "", "", reason)
		}
	}
	for _, value := range plan.Values {
		detail := value.Action
		if value.Action == "prune" {
			detail = "prune stored value"
		}
		fmt.Fprintf(out, "  %s %-24s %s\n", actionColor(style, "value"), value.Name, detail)
	}
	if plan.Empty() {
		fmt.Fprintln(out, "  "+style.Dim("no changes"))
	} else if !plan.Destructive() {
		fmt.Fprintln(out, "\n"+style.Dim("no destructive changes"))
	}
}

// printOrphanedValues surfaces the stored values the manifest no longer
// references. Deployments ignore them; --prune-values turns them into plan
// rows and removes them, so the hint names that flag in every flow that
// prints a plan (deploy, dev, promote) instead of a separate command.
// printRequiredRole names the environment role the plan needs (deploy for
// code-only, maintain when the definition or values change), when the
// server reported it. Only skali plan prints it: a deploy that got this far
// holds the role, and dev output stays lean.
func printRequiredRole(out io.Writer, role string) {
	if role == "" {
		return
	}
	style := clirender.StyleFor(out)
	fmt.Fprintf(out, "%s     %s\n", style.Dim("requires"), style.Dim(role+" on the environment"))
}

func printOrphanedValues(out io.Writer, orphaned []string) {
	if len(orphaned) == 0 {
		return
	}
	style := clirender.StyleFor(out)
	fmt.Fprintf(out, "\n  %s\n", style.Yellow("warning: ignoring stored values no longer referenced by the manifest: "+
		strings.Join(orphaned, ", ")+" (re-run with --prune-values to remove them)"))
}

// printVolumeSizeWarning surfaces the server's advisory that the target
// cluster's storage driver cannot enforce declared volume sizes (the local
// driver has no volume quotas). Advisory only, never an error.
func printVolumeSizeWarning(out io.Writer, unenforced bool) {
	if !unenforced {
		return
	}
	style := clirender.StyleFor(out)
	fmt.Fprintf(out, "\n  %s\n", style.Yellow("warning: this cluster uses the local storage driver, which does not "+
		"enforce volume sizes; declared sizes are advisory"))
}

// healthHints names the applications whose compiled definition declares no
// readiness probe: their rollouts can only verify that pods run, so the
// rollout health guarantee is weak. Advisory only, never an error.
func healthHints(result *compiler.Result) []string {
	var hints []string
	for _, key := range utils.SortedKeys(result.Definition.Applications) {
		if result.Definition.Applications[key].Health.Readiness.HTTP.Path == "" {
			hints = append(hints,
				"hint: application "+key+" declares no health check; rollouts cannot verify readiness")
		}
	}
	return hints
}

// printHealthHints renders the advisory block after a plan.
func printHealthHints(out io.Writer, result *compiler.Result) {
	hints := healthHints(result)
	if len(hints) == 0 {
		return
	}
	style := clirender.StyleFor(out)
	fmt.Fprintln(out)
	for _, hint := range hints {
		fmt.Fprintln(out, "  "+style.Dim(hint))
	}
}

// actionColor paints a plan action word in its own column: additions
// green, removals red, everything else neutral.
func actionColor(style *clirender.Style, action string) string {
	padded := fmt.Sprintf("%-7s", action)
	switch action {
	case "create":
		return style.Green(padded)
	case "remove", "delete", "destroy":
		return style.Red(padded)
	case "update", "replace":
		return style.Yellow(padded)
	case "value":
		return style.Dim(padded)
	}
	return padded
}

// confirmDestructive requires the environment name typed back.
func confirmDestructive(ctx context.Context, out io.Writer, environment string) (bool, error) {
	return cliprompt.New(os.Stdin, out).ConfirmTyped(ctx,
		"This plan is destructive",
		fmt.Sprintf("Type %q exactly to continue.", environment),
		environment)
}

// confirmPlan applies the deploy confirmation policy: destructive plans
// demand the typed environment name or the explicit flag, ordinary plans a
// simple yes. It flips request.AllowDestructive once confirmed.
func confirmPlan(ctx context.Context, out io.Writer, opts *deployOptions,
	planned *client.PlanResult, request *client.DeployRequest) error {
	if planned.Plan.Destructive() && !opts.AllowDestructive {
		if cliprompt.Interactive() && !opts.Yes {
			confirmed, err := confirmDestructive(ctx, out, opts.Environment)
			if err != nil {
				return err
			}
			if !confirmed {
				return errors.New("aborted")
			}
			request.AllowDestructive = true
		} else {
			return errors.New("plan is destructive; review it and re-run with --allow-destructive")
		}
	} else if planned.Plan.Destructive() {
		request.AllowDestructive = true
	} else if !opts.Yes {
		if !cliprompt.Interactive() {
			return errors.New("non-interactive use requires --yes")
		}
		confirmed, err := cliprompt.New(os.Stdin, out).Confirm(ctx, cliprompt.ConfirmOptions{
			Title: "Continue with this deployment?",
		})
		if err != nil {
			return err
		}
		if !confirmed {
			return errors.New("aborted")
		}
	}
	return nil
}

// taskSink mirrors engine progress into the live task display. Engine
// output stays on this machine: a build's lines reach the server only in
// its insights report, uploaded once the build ended.
type taskSink struct {
	task *clirender.Task
}

func (s *taskSink) Line(_, message string) {
	s.task.Note(message)
}

// insightsSink shows a build's progress live and records it for the
// build's insights report.
type insightsSink struct {
	taskSink
	recorder *build.InsightsRecorder
}

func (s *insightsSink) Line(level, message string) {
	s.recorder.Line(level, message)
	s.taskSink.Line(level, message)
}

// insightsUploadTimeout bounds the upload of one insights report; a slow
// server costs the report, never the deployment.
var insightsUploadTimeout = 30 * time.Second

// insightsUpload is one build's insights report on its way to the server,
// in parallel with the image upload.
type insightsUpload struct {
	application string
	done        chan struct{}
	err         error
}

// startInsightsUpload uploads report into the build step in the
// background.
func startInsightsUpload(ctx context.Context, api *client.Client, stepID, application string,
	report build.Insights) *insightsUpload {
	upload := &insightsUpload{application: application, done: make(chan struct{})}
	go func() {
		defer close(upload.done)
		ctx, cancel := context.WithTimeout(ctx, insightsUploadTimeout)
		defer cancel()
		upload.err = api.PutBuildInsights(ctx, stepID, report)
	}()
	return upload
}

// wait settles the upload; a report the server did not take is noted as
// a skipped task and never fails the deployment.
func (u *insightsUpload) wait(tasks *clirender.Tasks) {
	if u == nil {
		return
	}
	<-u.done
	if u.err == nil || errors.Is(u.err, context.Canceled) {
		// An interrupted deployment says so itself.
		return
	}
	reason := u.err.Error()
	if client.IsNotFound(u.err) {
		// A server from before build insights.
		reason = "the server does not keep build insights"
	}
	tasks.Start("Build insights for " + u.application).Skip("not saved: " + reason)
}

// insightsSummary is the settled build task's detail.
func insightsSummary(report build.Insights) string {
	cached := 0
	for _, step := range report.Steps {
		if step.Status == build.StepCached {
			cached++
		}
	}
	if cached == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d steps cached", cached, len(report.Steps))
}

// uploadSink follows a registry push in its live task: snapshots drive
// the progress bar, and the push's rate-limited lines are narrated on
// plain output.
type uploadSink struct {
	taskSink

	mu   sync.Mutex
	last build.UploadStatus
}

func newUploadSink(task *clirender.Task) *uploadSink {
	task.Narrate()
	return &uploadSink{taskSink: taskSink{task: task}}
}

func (s *uploadSink) UploadProgress(status build.UploadStatus) {
	s.task.Progress(uploadProgress(status))
	s.mu.Lock()
	s.last = status
	s.mu.Unlock()
}

// summary is the settled task's detail: the digest and what moved.
func (s *uploadSink) summary(digest string) string {
	s.mu.Lock()
	status := s.last
	s.mu.Unlock()
	parts := []string{utils.ShortChecksum(digest)}
	switch {
	case status.Sent > 0:
		parts = append(parts, "sent "+utils.FormatBytes(status.Sent))
		if status.Reused > 0 {
			parts = append(parts, utils.FormatBytes(status.Reused)+" already in the registry")
		}
	case status.ReusedBlobs > 0:
		parts = append(parts, "already in the registry")
	}
	return strings.Join(parts, ", ")
}

// uploadIdleNotice is how long a push may go without progress before the
// live display says so.
const uploadIdleNotice = 5 * time.Second

// uploadProgress renders a push snapshot for the live task display. The
// bar covers only the bytes that need sending: blobs the registry already
// had are reported beside it, never as transferred.
func uploadProgress(status build.UploadStatus) clirender.TaskProgress {
	var progress clirender.TaskProgress
	// The detail line truncates on narrow terminals, so what tells a stall
	// from a slow push comes first.
	var details []string
	if status.Idle >= uploadIdleNotice {
		details = append(details, build.IdleNote(status))
	}
	if status.Retrying {
		details = append(details, "retrying after: "+status.LastError)
	}
	switch status.Phase {
	case build.UploadPreparing:
		progress.Status = "preparing the image"
	case build.UploadChecking:
		progress.Status = "checking which blobs the registry already has"
	default:
		progress.Fraction, progress.HasFraction = status.Fraction()
		var parts []string
		needed, known := status.Needed()
		switch {
		case known && needed == 0:
			parts = append(parts, "nothing to send")
		case known:
			parts = append(parts, fmt.Sprintf("%3d%%", int(progress.Fraction*100)),
				utils.FormatBytes(status.Sent)+" / "+utils.FormatBytes(needed))
		default:
			parts = append(parts, utils.FormatBytes(status.Sent)+" sent")
		}
		// A stalled push shows how long nothing moved instead of a
		// decaying rate.
		if status.Phase == build.UploadSending && status.Rate > 0 && status.Idle < uploadIdleNotice {
			parts = append(parts, build.FormatRate(status.Rate))
		}
		if eta, ok := status.ETA(); ok {
			parts = append(parts, "ETA "+eta.String())
		}
		progress.Status = strings.Join(parts, "  ")
		if status.Phase == build.UploadFinalizing {
			details = append(details, "writing the manifest")
		}
	}
	if status.ReusedBlobs > 0 {
		details = append(details, utils.FormatBytes(status.Reused)+" already in the registry")
	}
	progress.Detail = strings.Join(details, " · ")
	return progress
}

// registryUsername is the Basic username presented to the registry token
// endpoint. It is cosmetic: the endpoint authenticates the password (a
// session token) and derives the subject from the session, ignoring the
// username entirely.
const registryUsername = "skali-session"

// executeActions performs the client side of the artifact window: builds,
// their uploads, and imports with journaled steps, heartbeats, and server
// verification. registryAuth authenticates every managed-registry push
// in-process; nil pushes anonymously (the local registry never challenges).
func executeActions(ctx context.Context, out io.Writer, api *client.Client,
	opened *client.OpenedDeployment, project *localProject, contexts map[string]*build.Context,
	registryAuth authn.Authenticator, rebuild bool) error {

	engine := &build.Docker{Auth: registryAuth}
	tasks := clirender.NewTasks(out)
	runID := opened.Deployment.RunID
	for _, action := range opened.Actions {
		switch action.Action {
		case "reuse":
			tasks.Start("artifact for " + action.Application).
				Skip("current, " + utils.ShortChecksum(action.Digest))
			continue
		case "build":
			err := artifactStep(ctx, api, runID, action, func() error {
				stopHeartbeat := startHeartbeat(ctx, api, action.BuildID)
				built, insights, err := buildAction(ctx, api, tasks, engine, opened, project, contexts, action, rebuild)
				if err != nil {
					stopHeartbeat()
					return err
				}
				digest, err := uploadAction(ctx, api, tasks, opened, action, built, insights)
				_ = built.Close()
				stopHeartbeat()
				if err != nil {
					return err
				}
				return verifyAction(ctx, api, opened, action, digest)
			})
			if err != nil {
				return err
			}
		case "import":
			err := artifactStep(ctx, api, runID, action, func() error {
				importStep, err := api.EnsureStep(ctx, runID, action.StepKey+".import",
					"Import "+action.Upstream, action.StepKey)
				if err != nil {
					return err
				}
				if err := api.SetStepStatus(ctx, importStep.ID, "running"); err != nil {
					return err
				}
				task := tasks.Start("Import " + action.Upstream)
				upload := newUploadSink(task)
				result, err := build.Import(ctx, action.Upstream, action.PushRef,
					build.ImportOptions{TargetAuth: registryAuth}, upload)
				if err != nil && ctx.Err() != nil {
					task.Cancel()
					return ctx.Err()
				}
				if err != nil {
					task.Fail()
					_ = api.SetStepStatus(ctx, importStep.ID, "failed")
					return failDeployment(ctx, api, opened, fmt.Errorf("import for %s failed: %w", action.Application, err))
				}
				task.Done(upload.summary(result.Digest))
				if err := api.SetStepStatus(ctx, importStep.ID, "succeeded"); err != nil {
					return err
				}
				return verifyAction(ctx, api, opened, action, result.Digest)
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// buildAction journals the local build of one action and returns its
// export, which the caller uploads and closes, and the upload of its
// insights report, which the caller waits for. A failed build uploads its
// report before it fails the deployment; an interrupted one has none.
func buildAction(ctx context.Context, api *client.Client, tasks *clirender.Tasks, engine build.Engine,
	opened *client.OpenedDeployment, project *localProject, contexts map[string]*build.Context,
	action client.ArtifactAction, rebuild bool) (build.Built, *insightsUpload, error) {
	application := project.Result.Definition.Applications[action.Application]
	spec := application.Source.Build
	step, err := api.EnsureStep(ctx, opened.Deployment.RunID, action.StepKey+".build",
		"Build locally ("+action.Platform+")", action.StepKey)
	if err != nil {
		return nil, nil, err
	}
	if err := api.SetStepStatus(ctx, step.ID, "running"); err != nil {
		return nil, nil, err
	}
	task := tasks.Start(fmt.Sprintf("Build %s (%s)", action.Application, action.Platform))
	recorder := build.NewInsightsRecorder(build.EngineDockerBuildx, action.Platform)
	built, err := engine.Build(ctx, build.BuildRequest{
		ContextDir: contexts[action.Application].Dir,
		Dockerfile: filepath.Join(project.Root, spec.Dockerfile),
		Target:     spec.Target,
		Arguments:  spec.Arguments,
		Platform:   action.Platform,
		PushRef:    action.PushRef,
		Rebuild:    rebuild,
	}, &insightsSink{taskSink: taskSink{task: task}, recorder: recorder})
	if err != nil && endsWithin(ctx, buildInterruptGrace) {
		// Interrupted, not failed: whoever ended the context closes the
		// window.
		task.Cancel()
		return nil, nil, ctx.Err()
	}
	report := recorder.Finish(err)
	if err != nil {
		task.Fail()
		startInsightsUpload(ctx, api, step.ID, action.Application, report).wait(tasks)
		_ = api.SetStepStatus(ctx, step.ID, "failed")
		return nil, nil, failDeployment(ctx, api, opened, fmt.Errorf("build for %s failed: %w", action.Application, err))
	}
	task.Done(insightsSummary(report))
	if err := api.SetStepStatus(ctx, step.ID, "succeeded"); err != nil {
		_ = built.Close()
		return nil, nil, err
	}
	return built, startInsightsUpload(ctx, api, step.ID, action.Application, report), nil
}

// uploadAction journals the push of a local build to the managed registry
// as its own step and returns the pushed digest. The step succeeds only
// once the registry accepted the manifest. It settles the build's pending
// insights upload once the push returned.
func uploadAction(ctx context.Context, api *client.Client, tasks *clirender.Tasks,
	opened *client.OpenedDeployment, action client.ArtifactAction, built build.Built, insights *insightsUpload) (string, error) {
	registry := build.RegistryOf(action.PushRef)
	step, err := api.EnsureStep(ctx, opened.Deployment.RunID, action.StepKey+".upload",
		"Upload to "+registry, action.StepKey)
	if err != nil {
		return "", err
	}
	if err := api.SetStepStatus(ctx, step.ID, "running"); err != nil {
		return "", err
	}
	task := tasks.Start("Upload " + action.Application + " to " + registry)
	upload := newUploadSink(task)
	result, err := built.Push(ctx, upload)
	// The build's report uploads beside the push; it lands before a
	// failed push fails the deployment, which would refuse it.
	insights.wait(tasks)
	if err != nil && ctx.Err() != nil {
		task.Cancel()
		return "", ctx.Err()
	}
	if err != nil {
		task.Fail()
		_ = api.SetStepStatus(ctx, step.ID, "failed")
		return "", failDeployment(ctx, api, opened, fmt.Errorf("upload for %s failed: %w", action.Application, err))
	}
	task.Done(upload.summary(result.Digest))
	if err := api.SetStepStatus(ctx, step.ID, "succeeded"); err != nil {
		return "", err
	}
	return result.Digest, nil
}

// buildInterruptGrace is how long a failed build waits for an interrupt
// before it counts as a failure. Ctrl-C reaches docker and this process at
// once, and a docker that dies of it can return before the signal ends ctx;
// failing the deployment then would record the interrupt as a broken
// build. A variable so tests can widen it.
var buildInterruptGrace = 250 * time.Millisecond

// endsWithin reports whether ctx ends within grace.
func endsWithin(ctx context.Context, grace time.Duration) bool {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-timer.C:
		return false
	}
}

// artifactStep journals one application's step under "artifacts" around
// its build or import and verify children: running from the first child on,
// then their outcome. A step left pending would be closed as skipped when
// the run finishes, above children that succeeded.
func artifactStep(ctx context.Context, api *client.Client, runID string,
	action client.ArtifactAction, children func() error) error {
	step, err := api.EnsureStep(ctx, runID, action.StepKey, action.Application, "artifacts")
	if err != nil {
		return err
	}
	if err := api.SetStepStatus(ctx, step.ID, "running"); err != nil {
		return err
	}
	if err := children(); err != nil {
		_ = api.SetStepStatus(ctx, step.ID, "failed")
		return err
	}
	return api.SetStepStatus(ctx, step.ID, "succeeded")
}

// verifyAction journals the verification step around the server's registry
// check.
func verifyAction(ctx context.Context, api *client.Client, opened *client.OpenedDeployment,
	action client.ArtifactAction, digest string) error {
	step, err := api.EnsureStep(ctx, opened.Deployment.RunID, action.StepKey+".verify",
		"Verify "+utils.ShortChecksum(digest), action.StepKey)
	if err != nil {
		return err
	}
	if err := api.SetStepStatus(ctx, step.ID, "running"); err != nil {
		return err
	}
	if _, err := api.VerifyArtifact(ctx, action.ArtifactID, opened.Deployment.ID, digest); err != nil {
		_ = api.SetStepStatus(ctx, step.ID, "failed")
		return failDeployment(ctx, api, opened, fmt.Errorf("verify %s: %w", action.Application, err))
	}
	return api.SetStepStatus(ctx, step.ID, "succeeded")
}

// failDeployment reports the client-side failure and returns the original
// error; values, target, and active revision stay untouched server-side.
// A report the server never received comes back as an
// *unreportedFailure: the deployment then still holds the environment.
func failDeployment(ctx context.Context, api *client.Client, opened *client.OpenedDeployment, cause error) error {
	if err := api.FailDeployment(ctx, opened.Deployment.ID, cause.Error()); err != nil {
		return &unreportedFailure{cause: cause, report: err}
	}
	return cause
}

// unreportedFailure is a client-side deployment failure the server was not
// told about, typically because the connection that failed the upload
// failed the report too. Its run keeps the environment's operation slot
// until someone cancels it or the server's stale-build sweep fails it.
type unreportedFailure struct {
	cause, report error
}

func (e *unreportedFailure) Error() string {
	return fmt.Sprintf("%v (additionally, failing the deployment: %v)", e.cause, e.report)
}

func (e *unreportedFailure) Unwrap() error { return e.cause }

// reportArtifactFailure prints a failed artifact window and what it left
// behind. Only a failure the server recorded left the environment as it
// was; an unreported one still holds it, so the run and the command that
// frees it are named.
func reportArtifactFailure(out io.Writer, runID, remoteHint string, err error) {
	style := clirender.StyleFor(out)
	fmt.Fprintf(out, "\n%srun %s %s: %v\n", style.Cross(), runID, style.Red("failed"), err)
	var unreported *unreportedFailure
	if errors.As(err, &unreported) {
		fmt.Fprintln(out, "\n"+style.Yellow("The server was not told: run "+runID+" still holds the environment, "+
			"blocking other deployments until it is cancelled or the server fails it for lack of build "+
			"progress (after 30 minutes by default). The active revision is untouched."))
		fmt.Fprintf(out, "  cancel  %s\n", runCancelHint(remoteHint, runID))
		return
	}
	fmt.Fprintln(out, "\n"+style.Dim("The environment is unchanged: staged values discarded, target and active revision untouched."))
}

// windowSignals end the artifact window: Ctrl-C, the terminal going away,
// and a polite kill all leave the local builds and pushes unfinished.
var windowSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}

// windowCancelTimeout bounds the cancel request of an interrupted window:
// the user asked to stop, so a server that does not answer must not hold
// the terminal. The stale-build sweep remains the backstop.
const windowCancelTimeout = 15 * time.Second

// windowWindDown is how long an interrupted window waits for its local
// work to stop before it reports and the process exits: a docker child
// that keeps the build output open must not hold the terminal. A variable
// so tests can shorten it.
var windowWindDown = 3 * time.Second

// artifactWindow is the part of a deployment that needs this process: from
// the open request through the builds, pushes, and verifications to the
// hand-over of the run to the server. The run cannot go on without it, so
// there is nothing to detach from: a window signal ends the window's
// context and cancels the deployment, which otherwise would hold the
// environment until the server's stale-build sweep fails it. A window the
// caller does not own (the dev session keeps its signals) passes its
// context through and catches nothing.
type artifactWindow struct {
	parent  context.Context
	ctx     context.Context
	cancel  context.CancelFunc
	signals chan os.Signal // nil when not owned
	quit    chan struct{}
	exited  chan struct{}
	caught  bool
	once    sync.Once
}

func watchArtifactWindow(ctx context.Context, own bool) *artifactWindow {
	w := &artifactWindow{parent: ctx}
	w.ctx, w.cancel = context.WithCancel(ctx)
	if !own {
		return w
	}
	w.signals = make(chan os.Signal, 1)
	w.quit = make(chan struct{})
	w.exited = make(chan struct{})
	signal.Notify(w.signals, windowSignals...)
	go func() {
		defer close(w.exited)
		select {
		case <-w.signals:
			w.caught = true
			w.cancel()
		case <-w.quit:
		}
	}()
	return w
}

// release lets the window's signals go and reports whether one arrived
// while they were held. Delivery is asynchronous, so the context alone
// can miss a signal that just landed; once signal.Stop returns nothing is
// on its way to the channel and the watcher has exited, so the answer is
// final.
func (w *artifactWindow) release() bool {
	w.once.Do(func() {
		if w.signals != nil {
			signal.Stop(w.signals)
			close(w.quit)
			<-w.exited
			select {
			case <-w.signals:
				w.caught = true
			default:
			}
		}
		w.cancel()
	})
	return w.caught
}

// errOpenInterrupted ends a command interrupted while its open request
// opened nothing.
var errOpenInterrupted = errors.New("interrupted")

// openArtifactWindow sends the open request with the window signals
// already held: the server may open the window before its answer arrives,
// so a signal meanwhile must not end the process, and the request runs to
// that answer, which names the run to cancel. An answer that opens nothing
// lets the signals go again, so Ctrl-C still ends a reauthentication
// prompt between attempts; a signal caught meanwhile ends the command.
func openArtifactWindow(ctx context.Context, own bool,
	open func() (*client.OpenedDeployment, error)) (*client.OpenedDeployment, *artifactWindow, error) {
	window := watchArtifactWindow(ctx, own)
	opened, err := open()
	if err == nil && !opened.UpToDate {
		return opened, window, nil
	}
	if window.release() {
		return nil, nil, errOpenInterrupted
	}
	return opened, nil, err
}

// run runs work, the rest of the window, and cancels the deployment when
// a signal ends it. The cancel request goes out at once, beside the work
// winding down, so the run's fate never waits on a docker slow to stop;
// the work gets windowWindDown to settle its display before the outcome is
// printed. Signals stay caught until then, so a second Ctrl-C cannot end
// the process before the request is sent.
func (w *artifactWindow) run(out io.Writer, api *client.Client, runID, remoteHint string,
	work func(ctx context.Context) error) error {
	if w.signals != nil {
		// Held beyond release, which must let the window's own channel go
		// to know for certain whether a signal arrived.
		hold := make(chan os.Signal, 1)
		signal.Notify(hold, windowSignals...)
		defer signal.Stop(hold)
	}
	done := make(chan error, 1)
	go func() {
		err := w.ctx.Err()
		if err == nil {
			err = work(w.ctx)
		}
		done <- err
	}()
	var err error
	finished := false
	select {
	case err = <-done:
		finished = true
	case <-w.ctx.Done():
	}
	if !w.release() {
		// Done, or the caller's context ended, which is the caller's to
		// close; its work stops with it.
		if !finished {
			err = <-done
		}
		return err
	}
	cancelled := make(chan windowCancel, 1)
	go func() { cancelled <- requestWindowCancel(w.parent, api, runID) }()
	if !finished {
		select {
		case <-done:
		case <-time.After(windowWindDown):
		}
	}
	style := clirender.StyleFor(out)
	fmt.Fprintf(out, "\n%s\n", style.Yellow("interrupted: cancelling run "+runID))
	return reportWindowCancel(out, <-cancelled, runID, remoteHint)
}

// windowCancel is the answer to an interrupted window's cancel request.
// Concluded marks an error no retry could change: the run ended first, or
// its rollout activated meanwhile.
type windowCancel struct {
	outcome   *client.CancelOutcome
	err       error
	concluded bool
}

// requestWindowCancel cancels the run of an interrupted window. Usually
// the window was still open; a signal during the hand-over may find the
// run promoted, which the cancel undoes like any other. The request runs
// on a bounded context of its own, the signal having ended the flow's.
func requestWindowCancel(ctx context.Context, api *client.Client, runID string) windowCancel {
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), windowCancelTimeout)
	defer cancel()
	outcome, err := api.CancelRun(cancelCtx, runID)
	if !isRunAlreadyFinished(err) {
		return windowCancel{outcome: outcome, err: err}
	}
	// A conflict is a run that ended first, or a hand-over that promoted
	// the deployment between the server's read and its cancel. Only the
	// run's state tells them apart, and a run still going takes one more
	// request, which stops the promoted rollout.
	tree, getErr := api.GetRun(cancelCtx, runID)
	if getErr != nil {
		return windowCancel{err: fmt.Errorf("%w (reading the run: %v)", err, getErr)}
	}
	if runSettled(tree) {
		return windowCancel{err: err, concluded: true}
	}
	outcome, err = api.CancelRun(cancelCtx, runID)
	return windowCancel{outcome: outcome, err: err, concluded: isRunAlreadyFinished(err)}
}

// reportWindowCancel prints what the cancel did to the environment and
// returns the command's closing error. A cancel that may not have landed
// names the run and the command to retry it.
func reportWindowCancel(out io.Writer, result windowCancel, runID, remoteHint string) error {
	style := clirender.StyleFor(out)
	switch {
	case result.err != nil && result.concluded:
		return fmt.Errorf("run %s: %w", runID, result.err)
	case result.err != nil:
		return fmt.Errorf("cancel run %s: %w; retry with %s", runID, result.err, runCancelHint(remoteHint, runID))
	case result.outcome.Continues:
		printFirstDeploymentContinues(out, style)
	case result.outcome.Fallback:
		fmt.Fprintln(out, style.Dim("The environment returns to its active revision."))
	default:
		fmt.Fprintln(out, style.Dim("The environment is unchanged: staged values discarded, target and active revision untouched."))
	}
	return fmt.Errorf("run %s was cancelled", runID)
}

func startHeartbeat(ctx context.Context, api *client.Client, buildID string) (stop func()) {
	heartbeatCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				_, _ = api.HeartbeatBuild(heartbeatCtx, buildID)
			}
		}
	}()
	return func() { cancel(); <-done }
}

// runDeployFlow is the transcript loop shared by skali plan, deploy, and
// dev. planOnly stops after printing the server-computed plan.
// Deploy flow outcomes: what actually happened, so callers like bare
// skali dev can decide whether following runtime logs is welcome (a user
// who already detached with Ctrl-C is not asking for more output).
const (
	deployOutcomePlanned  = "planned"
	deployOutcomeUpToDate = "up-to-date"
	deployOutcomeReady    = "ready"
	deployOutcomeDetached = "detached"
)

// deployTarget is the resolved destination of one plan, deploy, or dev
// invocation.
type deployTarget struct {
	remoteName    string
	master        string
	api           *client.Client
	projectID     string
	environmentID string
	// access is the caller's effective role on the environment as the
	// server reported it (admin for one just created); empty when the
	// server did not say (older fakes), which disables client-side checks.
	access string
	// settings are the environment's server-side settings (protection
	// policy among them); nil when the environment is locked or the server
	// did not say, which disables the client-side policy check.
	settings *client.EnvironmentSettings
	// sessionToken doubles as the managed-registry push credential: the
	// registry token endpoint accepts it as the Basic password, so builds
	// and imports authenticate without any docker login.
	sessionToken string
}

// resolveDeployTarget resolves remote, project, and environment, creating
// project and environment per policy (interactive deploy behind explicit
// confirmation, dev silently, plan and non-interactive never), and links
// the checkout on success. prompts is cliprompt.Interactive() && !opts.Yes.
func resolveDeployTarget(ctx context.Context, out io.Writer, in *bufio.Reader,
	project *localProject, opts *deployOptions, planOnly, prompts bool) (*deployTarget, error) {

	style := clirender.StyleFor(out)
	projectName := project.Result.Definition.Name

	cfg, err := cliconfig.Load()
	if err != nil {
		return nil, err
	}
	selected, err := resolveRemoteTarget(cfg, project.Path, project.Root, opts.Remote)
	if err != nil {
		return nil, err
	}
	remoteName, remote := selected.Name, selected.Remote
	binding := selected.Binding
	if !opts.UseBinding {
		binding = nil
	}
	if binding != nil && binding.Project != projectName {
		return nil, fmt.Errorf("this checkout is linked to project %s but the manifest names %s; fix the manifest name or delete .skali/target.yaml to relink", binding.Project, projectName)
	}

	api := remoteClient(cfg, remote)

	if opts.UseBinding {
		printHeader(out, style, headerRow{"remote", remoteName, remote.Master})
	}
	printHeader(out, style, headerRow{"project", projectName, filepath.Base(project.Path)})

	// The bound environment is the default; --environment overrides it for
	// one invocation without rewriting the binding.
	if opts.Environment == "" && binding != nil {
		opts.Environment = binding.Environment
	}

	proj, err := findProject(ctx, api, projectName)
	if err != nil {
		return nil, err
	}
	projectID := ""
	switch {
	case proj != nil:
		projectID = proj.ID
	case planOnly:
		return nil, fmt.Errorf("project %s does not exist on %s; "+
			"skali plan never changes the installation, run skali deploy to create it",
			projectName, remote.Master)
	case opts.CreateMissing:
		created, err := api.CreateProject(ctx, projectName)
		if err != nil {
			return nil, err
		}
		projectID = created.ID
	case !prompts:
		return nil, fmt.Errorf("project %s does not exist on %s; "+
			"run skali deploy interactively to create it", projectName, remote.Master)
	default:
		confirmed, err := promptSession(out, in).Confirm(ctx, cliprompt.ConfirmOptions{
			Title: fmt.Sprintf("Create project %s on %s?", projectName, remoteName),
		})
		if err != nil {
			return nil, err
		}
		if !confirmed {
			return nil, errors.New("aborted")
		}
		created, err := api.CreateProject(ctx, projectName)
		if err != nil {
			return nil, err
		}
		projectID = created.ID
	}

	environment, err := resolveEnvironmentTarget(ctx, out, in, api,
		projectID, projectName, remote.Master, opts, planOnly, prompts)
	if err != nil {
		return nil, err
	}

	// Link the checkout on first contact, but only when the remote came
	// from the config's current selection: an explicit --remote (including
	// dev's disposable local remote) is one-shot and never bound.
	if opts.UseBinding && binding == nil && opts.Remote == "" {
		if err := checkout.Save(project.Root, &checkout.Target{
			Master:      remote.Master,
			Project:     projectName,
			Environment: opts.Environment,
		}); err != nil {
			return nil, err
		}
		fmt.Fprintln(out, style.Dim(fmt.Sprintf(
			"linked to remote %s, project %s, environment %s; stored in .skali/",
			remoteName, projectName, opts.Environment)))
	}

	return &deployTarget{
		remoteName:    remoteName,
		master:        remote.Master,
		api:           api,
		projectID:     projectID,
		environmentID: environment.ID,
		access:        environment.Access,
		settings:      environment.Settings,
		sessionToken:  remote.Token,
	}, nil
}

// resolveEnvironmentTarget resolves or creates (per the same policy as the
// project: interactive deploy behind explicit confirmation, dev silently,
// plan and non-interactive never) the environment named by opts.Environment,
// prompting for a name when empty. It prints the environment line and
// returns the environment with the caller's access and its settings.
func resolveEnvironmentTarget(ctx context.Context, out io.Writer, in *bufio.Reader, api *client.Client,
	projectID, projectName, master string, opts *deployOptions, planOnly, prompts bool) (*client.Environment, error) {

	style := clirender.StyleFor(out)
	environments, err := api.ListEnvironments(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if opts.Environment == "" {
		switch {
		case !prompts:
			return nil, errors.New("--environment is required")
		case len(environments) == 0 && planOnly:
			return nil, fmt.Errorf("project %s has no environments on %s; run skali deploy to create one",
				projectName, master)
		case len(environments) == 0:
			opts.Environment, err = promptSession(out, in).Text(ctx, cliprompt.TextOptions{
				Title:   "Environment name",
				Default: "production",
				Validate: func(value string) error {
					if value == "" {
						return errors.New("environment name is required")
					}
					return nil
				},
			})
			if err != nil {
				return nil, err
			}
		default:
			environment, err := chooseEnvironment(out, in, environments)
			if err != nil {
				return nil, err
			}
			opts.Environment = environment
		}
	}

	environment := findEnvironment(environments, opts.Environment)
	switch {
	case environment != nil:
	case planOnly:
		return nil, fmt.Errorf("environment %s does not exist in project %s on %s; "+
			"skali plan never changes the installation, run skali deploy to create it",
			opts.Environment, projectName, master)
	case opts.CreateMissing:
		if environment, err = api.CreateEnvironment(ctx, projectID, opts.Environment, ""); err != nil {
			return nil, err
		}
	case !prompts:
		return nil, fmt.Errorf("environment %s does not exist in project %s on %s; "+
			"run skali deploy interactively to create it", opts.Environment, projectName, master)
	default:
		confirmed, err := promptSession(out, in).Confirm(ctx, cliprompt.ConfirmOptions{
			Title: fmt.Sprintf("Create environment %s in project %s?",
				opts.Environment, projectName),
		})
		if err != nil {
			return nil, err
		}
		if !confirmed {
			return nil, errors.New("aborted")
		}
		if environment, err = api.CreateEnvironment(ctx, projectID, opts.Environment, ""); err != nil {
			return nil, err
		}
	}

	printHeader(out, style, headerRow{"environment", opts.Environment, ""})
	return environment, nil
}

func runDeployFlow(command *cobra.Command, opts *deployOptions, planOnly bool) (string, error) {
	ctx := command.Context()
	out := command.OutOrStdout()
	project, err := loadLocalProject(opts.Manifest, command.ErrOrStderr())
	if err != nil {
		return "", err
	}
	style := clirender.StyleFor(out)
	prompts := cliprompt.Interactive() && !opts.Yes
	// One stdin reader for the whole flow: the environment prompts and a
	// reauth prompt (a consumed protection bypass on an aged login) must
	// not race each other for buffered lines.
	in := bufio.NewReader(os.Stdin)
	target, err := resolveDeployTarget(ctx, out, in, project, opts, planOnly, prompts)
	if err != nil {
		return "", err
	}
	api, projectID, environmentID := target.api, target.projectID, target.environmentID
	if err := checkDeployAccess(target.access, opts.Environment); err != nil {
		return "", err
	}
	mayStage, err := valuesStagingAllowed(target.access, opts.Environment, opts)
	if err != nil {
		return "", err
	}
	if _, err := checkDeployPolicy(target.settings, target.access, opts.Environment, "", opts); err != nil {
		return "", err
	}
	definitionVersion, err := api.SubmitDefinition(ctx, projectID, string(project.Source), "yaml")
	if err != nil {
		return "", err
	}

	// Values: an explicit, remembered, or picked local file stages a
	// candidate; otherwise the environment's stored values apply. Below
	// maintain no file is consulted: the stored values are all this role
	// may use. The fingerprint of the staged set is recorded once the
	// deployment is committed, so the next run can say whether the file
	// changed since.
	var file *values.File
	candidateID := ""
	var excludeFiles []string
	recordFingerprint := func() {}
	if mayStage {
		// Dev's implied --yes only skips confirmations; its picker runs
		// whenever the terminal allows.
		selected, err := selectValues(out, in, project, opts, cliprompt.Interactive() && (!opts.Yes || opts.AutoEnvFile))
		if err != nil {
			return "", err
		}
		file = selected.file
		if file == nil {
			printHeader(out, style, headerRow{"values", "stored values", describeValuesSource(selected, project, "")})
			if !planOnly {
				recordFingerprint = func() {
					if err := checkout.SaveEnvFileFingerprint(project.Root, opts.Environment, ""); err != nil {
						fmt.Fprintf(out, "  %s\n", style.Yellow("warning: "+err.Error()))
					}
				}
			}
		} else {
			kept, missing, skipped := values.Conform(project.Result.Definition.RequiredVariables, file.Values)
			if len(missing) > 0 {
				return "", fmt.Errorf("%s: missing required project values: %s", file.Path, strings.Join(missing, ", "))
			}
			staging := fmt.Sprintf("%d values; validated, not uploaded", len(kept))
			if !planOnly {
				staged, err := api.StageValues(ctx, environmentID, kept, definitionVersion.DefinitionVersionID)
				if err != nil {
					return "", err
				}
				candidateID = staged.CandidateID
				staging = fmt.Sprintf("%d staged", len(staged.Staged))
				if len(staged.Skipped) > 0 {
					skipped = staged.Skipped
				}
				fingerprint := values.Fingerprint(kept)
				recordFingerprint = func() {
					if err := checkout.SaveEnvFileFingerprint(project.Root, opts.Environment, fingerprint); err != nil {
						fmt.Fprintf(out, "  %s\n", style.Yellow("warning: "+err.Error()))
					}
				}
			}
			printHeader(out, style, headerRow{"values", displayPath(project.Root, file.Path),
				describeValuesSource(selected, project, staging)})
			if len(skipped) > 0 {
				fmt.Fprintf(out, "  %s\n", style.Yellow("warning: skipped keys not referenced by the manifest: "+
					strings.Join(skipped, ", ")))
			}
			excludeFiles = append(excludeFiles, file.Path)
		}
	} else {
		printHeader(out, style, headerRow{"values", style.Dim("stored"),
			fmt.Sprintf("%s role cannot stage values", target.access)})
	}

	// The environment status is fetched before hashing because it carries
	// the cluster's node platforms, which are part of every input hash.
	var envStatus *client.EnvironmentStatus
	if status, err := api.EnvironmentStatus(ctx, environmentID); err == nil {
		envStatus = status
	} else {
		fmt.Fprintf(out, "  %s\n", style.Yellow(fmt.Sprintf(
			"warning: could not fetch environment status: %v", err)))
	}
	platforms, err := resolveAppPlatforms(out, envStatus, opts.Platform,
		project.Result.Definition, opts.LocalApplications)
	if err != nil {
		return "", err
	}
	inputs, contexts, err := buildInputs(project, excludeFiles, platforms, opts.LocalApplications)
	if err != nil {
		return "", err
	}
	request := client.DeployRequest{
		DefinitionVersionID: definitionVersion.DefinitionVersionID,
		CandidateID:         candidateID,
		BuildExecutor:       "local",
		Builds:              inputs,
		Force:               opts.Force,
		Rebuild:             opts.Rebuild,
		LocalApplications:   opts.LocalApplications,
		PruneValues:         opts.PruneValues,
		BypassProtection:    opts.BypassProtection,
	}

	activeChecksum := ""
	if envStatus != nil && envStatus.ActiveRevision != nil {
		activeChecksum = envStatus.ActiveRevision.Checksum
	}
	// Plan and open answer reauth_required only for a consumed protection
	// bypass on an aged login; the retry confirms the password once.
	var planned *client.PlanResult
	err = withReauth(ctx, out, in, api, func() (err error) {
		planned, err = api.Plan(ctx, environmentID, request)
		return err
	})
	if err != nil {
		return "", err
	}
	printPlan(out, planned.Plan, planned.Actions, activeChecksum)
	printOrphanedValues(out, planned.Orphaned)
	printVolumeSizeWarning(out, planned.VolumeSizesUnenforced)
	for _, warning := range planned.Warnings {
		fmt.Fprintln(out, "warning: "+warning.Message)
	}
	if planned.BypassProtection {
		printProtectionBypassed(out, opts.Environment)
	}
	if planOnly {
		printRequiredRole(out, planned.RequiredRole)
	}
	printHealthHints(out, project.Result)
	if planOnly {
		return deployOutcomePlanned, nil
	}
	if planned.UpToDate && !opts.Force {
		// Up to date means the candidate values equal the active ones:
		// what this file holds is what runs.
		recordFingerprint()
		fmt.Fprintln(out, "\nnothing to deploy")
		return deployOutcomeUpToDate, nil
	}
	if planned.UpToDate {
		fmt.Fprintln(out, "\nnothing changed; deploying anyway (--force restarts the application workloads)")
	}

	if err := confirmPlan(ctx, out, opts, planned, &request); err != nil {
		return "", err
	}

	// The dev session owns the signals and closes an interrupted window on
	// its own way out.
	ownWindow := opts.Attach != attachSessionEnds
	var opened *client.OpenedDeployment
	var window *artifactWindow
	err = withReauth(ctx, out, in, api, func() (err error) {
		opened, window, err = openArtifactWindow(ctx, ownWindow, func() (*client.OpenedDeployment, error) {
			return api.OpenDeployment(ctx, environmentID, request)
		})
		return err
	})
	if err != nil {
		return "", err
	}
	if opened.UpToDate {
		recordFingerprint()
		fmt.Fprintln(out, "\nnothing to deploy")
		return deployOutcomeUpToDate, nil
	}
	if opts.OnDeploymentOpened != nil {
		opts.OnDeploymentOpened(opened.Deployment.ID)
	}
	fmt.Fprintf(out, "\n%s %s  deploy %s to %s\n", style.Dim("run"),
		style.Bold(opened.Deployment.RunID), project.Result.Definition.Name, opts.Environment)

	// The remote session doubles as the registry push credential; builds
	// and imports push in-process with it, so docker never talks to the
	// managed registry and no credential touches its config or keychain.
	var registryAuth authn.Authenticator
	if target.sessionToken != "" {
		registryAuth = authn.FromConfig(authn.AuthConfig{
			Username: registryUsername, Password: target.sessionToken,
		})
	}
	err = window.run(out, api, opened.Deployment.RunID, opts.Remote, func(ctx context.Context) error {
		if err := executeActions(ctx, out, api, opened, project, contexts, registryAuth, opts.Rebuild); err != nil {
			if ctx.Err() != nil {
				// Interrupted, not failed: the window's owner reports it.
				return err
			}
			reportArtifactFailure(out, opened.Deployment.RunID, opts.Remote, err)
			return errors.New("deployment failed")
		}
		_, err := api.CompleteDeployment(ctx, opened.Deployment.ID)
		return err
	})
	if err != nil {
		return "", err
	}
	// The run is the server's from here: the staged values promote with
	// it, so the checkout remembers what it sent.
	recordFingerprint()
	if opts.OnDeploymentClosed != nil {
		opts.OnDeploymentClosed()
	}
	if opts.Detach {
		fmt.Fprintf(out, "deployment continues on the server; attach with: %s\n", runAttachHint(opts.Remote, opened.Deployment.RunID))
		return deployOutcomeDetached, nil
	}
	outcome, err := attachRunMode(ctx, out, api, opened.Deployment.RunID, opts.Remote, opts.Attach)
	if err != nil {
		return "", err
	}
	switch outcome.Status {
	case "succeeded":
		fmt.Fprintln(out, "\n"+style.Check()+style.Bold(style.Green("ready")))
		if !opts.SkipReadySummary {
			summary := remoteReadySummary(target.remoteName)
			summary.ProtectionBypassed = opened.BypassProtection
			printReadySummary(ctx, out, api, environmentID, summary)
		}
		return deployOutcomeReady, nil
	case "failed":
		return "", failedRunError(opened.Deployment.RunID, outcome.Failure)
	case "cancelled":
		return "", fmt.Errorf("run %s was cancelled", opened.Deployment.RunID)
	default:
		return deployOutcomeDetached, nil
	}
}
