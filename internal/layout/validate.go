package layout

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/Hinkolas/skali/internal/utils"
	"github.com/Hinkolas/skali/internal/yamldoc"
)

var stableKeyPattern = regexp.MustCompile("^[a-z][a-z0-9-]{0,62}$")

func Validate(document *Document) yamldoc.Diagnostics {
	l := document.Layout
	var diagnostics yamldoc.Diagnostics
	add := func(path, format string, args ...any) {
		diagnostics = append(diagnostics, document.Diagnostic(path, fmt.Sprintf(format, args...)))
	}

	if l.Version != CurrentVersion {
		add("version", "unsupported layout version %q; expected %q", l.Version, CurrentVersion)
	}
	if !stableKeyPattern.MatchString(l.Name) {
		add("name", "must start with a lowercase letter and contain only lowercase letters, numbers, and hyphens")
	}
	if len(l.Nodes) == 0 {
		add("nodes", "must declare at least one node")
		return diagnostics
	}

	servers := 0
	capable := make(map[string]bool)
	for _, key := range utils.SortedKeys(l.Nodes) {
		path := "nodes." + key
		node := l.Nodes[key]
		if !stableKeyPattern.MatchString(key) {
			add(path, "key must start with a lowercase letter and contain only lowercase letters, numbers, and hyphens")
		}
		switch node.Role {
		case RoleServer:
			servers++
		case RoleAgent:
			if len(node.Capabilities) == 0 {
				add(path, "an agent without capabilities cannot receive work; assign at least one or remove the node")
			}
		case "":
			add(path+".role", "is required")
		default:
			add(path+".role", "must be server or agent")
		}
		seen := make(map[string]bool)
		for index, capability := range node.Capabilities {
			capabilityPath := fmt.Sprintf("%s.capabilities[%d]", path, index)
			if !slices.Contains(Capabilities, capability) {
				add(capabilityPath, "unknown capability %q; known capabilities are %s",
					capability, strings.Join(Capabilities, ", "))
				continue
			}
			if seen[capability] {
				add(capabilityPath, "duplicate capability %q", capability)
				continue
			}
			seen[capability] = true
			capable[capability] = true
		}
	}

	if servers != 1 && servers != 3 {
		add("nodes", "requires exactly one server, or three servers for a highly available control plane; found %d", servers)
	}
	for _, capability := range RequiredCapabilities {
		if !capable[capability] {
			add("nodes", "no node carries the %s capability, which every installation requires", capability)
		}
	}

	return diagnostics
}
