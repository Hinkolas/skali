package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/Hinkolas/skali/internal/checkout"
	versionpkg "github.com/Hinkolas/skali/internal/version"
	"github.com/spf13/cobra"
)

const envVersionContext = "SKALI_VERSION_CONTEXT"
const minimumDispatchRelease = "v0.1.0-rc.3"

// versionContext is the additive, secret-free contract between the launcher
// and its release worker. Parent binds it to one child, not grandchildren.
type versionContext struct {
	Resolved bool   `json:"resolved,omitempty"`
	Handoffs int    `json:"handoffs,omitempty"`
	Home     string `json:"home,omitempty"`
	// HomeRelease is the launcher's own release. A worker runs at the
	// target's release, which may be older; policies about what home may
	// manage (a cluster upgrade target, for one) compare against this.
	HomeRelease string           `json:"homeRelease,omitempty"`
	Parent      int              `json:"parent"`
	Remote      string           `json:"remote,omitempty"`
	Master      string           `json:"master,omitempty"`
	Instance    string           `json:"instance,omitempty"`
	Release     string           `json:"release"`
	Source      string           `json:"source"`
	Mode        string           `json:"mode"`
	Binding     *checkout.Target `json:"binding,omitempty"`
}

var invocationContext *versionContext

func contextFromEnvironment() (*versionContext, error) {
	raw := os.Getenv(envVersionContext)
	if raw == "" {
		return nil, nil
	}
	var ctx versionContext
	if err := json.Unmarshal([]byte(raw), &ctx); err != nil {
		return nil, fmt.Errorf("invalid dispatched version context: %w", err)
	}
	if ctx.Parent != os.Getppid() {
		return nil, nil
	}
	if ctx.Release != versionpkg.Version {
		return nil, fmt.Errorf("dispatched CLI reports %s, expected %s", versionpkg.Version, ctx.Release)
	}
	return &ctx, nil
}

func contextEnvironment(environ []string, selected *versionContext) []string {
	var env []string
	for _, entry := range environ {
		if !strings.HasPrefix(entry, envVersionContext+"=") && !strings.HasPrefix(entry, envDispatched+"=") {
			env = append(env, entry)
		}
	}
	if selected != nil {
		copy := *selected
		copy.Parent = os.Getpid()
		raw, _ := json.Marshal(copy)
		env = append(env, envVersionContext+"="+string(raw))
	}
	return append(env, envDispatched+"=1")
}

// homeRelease is the release of the binary the user invoked: this one, or
// the launcher's when this process is its dispatched worker.
func homeRelease() string {
	if invocationContext != nil && invocationContext.HomeRelease != "" {
		return invocationContext.HomeRelease
	}
	return versionpkg.Version
}

func versionDescription() string {
	ctx := invocationContext
	if ctx == nil {
		return fmt.Sprintf("skali %s; target: none; source: this CLI; mode: home", versionpkg.Version)
	}
	target := ctx.Remote
	if target == "" {
		target = "none"
	}
	return fmt.Sprintf("skali %s; target: %s; source: %s; mode: %s", versionpkg.Version, target, ctx.Source, ctx.Mode)
}

func addVersionFlags(command *cobra.Command, manifestFlag bool) {
	command.Flags().String("remote", "", "target remote, overriding the checkout binding and current remote")
	command.Flags().Bool("offline", false, "use the recorded target release and an available CLI without contacting the remote or downloading")
	if manifestFlag {
		command.Flags().String("manifest", "", "manifest path used to select the checkout")
	}
}

// Ignore worker markers inherited by a grandchild or set without a validated
// invocation context. Only the direct, authenticated worker suppresses handoff.
func dispatchEnvironment(key string) string {
	if key == envDispatched && (invocationContext == nil || invocationContext.Parent != os.Getppid()) {
		return ""
	}
	return os.Getenv(key)
}
