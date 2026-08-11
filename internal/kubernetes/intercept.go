package kubernetes

import (
	"fmt"
	"strings"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/utils"
)

// ResolveInterceptPorts maps every service port the application renders to
// the host port a local dev process listens on. declared is keyed by
// manifest port name (the dev.ports authoring surface); synthesized
// route-<key> ports resolve through the manifest port carrying the same
// container port number. Built on renderServicePorts so intercept
// validation and rendering can never drift from the Service shape.
func ResolveInterceptPorts(application compiler.Application, declared map[string]int32) (map[string]int32, error) {
	for _, name := range utils.SortedKeys(declared) {
		if _, ok := application.Ports[name]; !ok {
			return nil, fmt.Errorf("declares a host port for unknown application port %q", name)
		}
	}
	numberHost := make(map[int32]int32, len(declared))
	for name, host := range declared {
		numberHost[int32(application.Ports[name].Port)] = host
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
