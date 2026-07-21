package limavm

import (
	"bytes"
	_ "embed"
	"fmt"
	"text/template"
)

//go:embed vm.yaml.tmpl
var vmTemplateText string

var vmTemplate = template.Must(template.New("vm.yaml").Parse(vmTemplateText))

// templateParams feeds vm.yaml.tmpl; ForwardPort is nonzero only on
// user-v2, where the Kubernetes API must ride an explicit loopback
// forward.
type templateParams struct {
	CPUs        int
	Memory      string
	Disk        string
	Network     Network
	ForwardPort int
	Hostname    string
}

func renderTemplate(spec Spec) ([]byte, error) {
	params := templateParams{
		CPUs:     spec.CPUs,
		Memory:   spec.Memory,
		Disk:     spec.Disk,
		Network:  spec.Network,
		Hostname: spec.Hostname,
	}
	if spec.Network == NetworkUserV2 {
		params.ForwardPort = KubeForwardPort
	}
	var rendered bytes.Buffer
	if err := vmTemplate.Execute(&rendered, params); err != nil {
		return nil, fmt.Errorf("render VM template: %w", err)
	}
	return rendered.Bytes(), nil
}
