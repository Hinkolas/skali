package kubernetes

import (
	"bytes"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

func MarshalYAML(objects []runtime.Object) ([]byte, error) {
	var output bytes.Buffer
	for index, object := range objects {
		jsonData, err := json.Marshal(object)
		if err != nil {
			return nil, fmt.Errorf("encode Kubernetes object %d as JSON: %w", index, err)
		}
		var desired map[string]any
		if err := json.Unmarshal(jsonData, &desired); err != nil {
			return nil, fmt.Errorf("normalize Kubernetes object %d: %w", index, err)
		}
		delete(desired, "status")
		jsonData, err = json.Marshal(desired)
		if err != nil {
			return nil, fmt.Errorf("normalize Kubernetes object %d as JSON: %w", index, err)
		}
		yamlData, err := yaml.JSONToYAML(jsonData)
		if err != nil {
			return nil, fmt.Errorf("encode Kubernetes object %d as YAML: %w", index, err)
		}
		if index > 0 {
			output.WriteString("---\n")
		}
		output.Write(yamlData)
	}
	return output.Bytes(), nil
}
