package kubernetes

import (
	"fmt"
	"strings"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/utils"
)

// InterceptPortNames lists the service port names an intercepted
// application must cover: manifest ports plus the synthesized route-<key>
// ports of numeric route targets. Built on renderServicePorts so the
// CLI's port allocation can never drift from the Service shape the
// server renders.
func InterceptPortNames(application compiler.Application) []string {
	rendered := renderServicePorts(application)
	names := make([]string, 0, len(rendered))
	for _, port := range rendered {
		names = append(names, port.Name)
	}
	return names
}

// ResolveInterceptPorts maps every service port the application renders to
// the host port a local dev process listens on. declared may be keyed by
// manifest port name (the dev.ports authoring surface) or by rendered
// service port name (the CLI's complete allocation, including synthesized
// route-<key> ports); a manifest-named entry also resolves the rendered
// ports carrying the same container port number. Built on
// renderServicePorts so intercept validation and rendering can never
// drift from the Service shape.
func ResolveInterceptPorts(application compiler.Application, declared map[string]int32) (map[string]int32, error) {
	rendered := make(map[string]struct{})
	for _, port := range renderServicePorts(application) {
		rendered[port.Name] = struct{}{}
	}
	for _, name := range utils.SortedKeys(declared) {
		if _, ok := application.Ports[name]; ok {
			continue
		}
		if _, ok := rendered[name]; ok {
			continue
		}
		return nil, fmt.Errorf("declares a host port for unknown application port %q", name)
	}
	numberHost := make(map[int32]int32, len(declared))
	for name, host := range declared {
		if port, ok := application.Ports[name]; ok {
			numberHost[int32(port.Port)] = host
		}
	}
	resolved := make(map[string]int32)
	var missing []string
	for _, port := range renderServicePorts(application) {
		if host, ok := declared[port.Name]; ok {
			resolved[port.Name] = host
			continue
		}
		if host, ok := numberHost[port.Port]; ok {
			resolved[port.Name] = host
			continue
		}
		missing = append(missing, port.Name)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing host ports for service ports %s; declare them in the application's dev.ports",
			strings.Join(missing, ", "))
	}
	return resolved, nil
}
