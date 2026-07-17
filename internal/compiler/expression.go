package compiler

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Hinkolas/skali/internal/manifest"
)

var expressionToken = regexp.MustCompile("\\$\\{([A-Z_][A-Z0-9_]*)(?::-([^}]*))?\\}|\\{\\{\\s*(databases|buckets)\\.([a-z][a-z0-9-]{0,62})\\.([a-z_][a-z0-9_]*)\\s*\\}\\}")

var outputCatalog = map[string]map[string]bool{
	"databases": {
		"host":     false,
		"port":     false,
		"name":     false,
		"username": true,
		"password": true,
		"url":      true,
	},
	"buckets": {
		"endpoint":   false,
		"name":       false,
		"region":     false,
		"access_key": true,
		"secret_key": true,
	},
}

func parseExpression(raw string, project manifest.Project, allowOutputs bool) (Expression, error) {
	var parts []ExpressionPart
	cursor := 0
	matches := expressionToken.FindAllStringSubmatchIndex(raw, -1)
	for _, match := range matches {
		if match[0] > cursor {
			literal := raw[cursor:match[0]]
			if containsExpressionMarker(literal) {
				return Expression{}, fmt.Errorf("contains a malformed variable or service-output expression")
			}
			parts = append(parts, ExpressionPart{Kind: "literal", Value: literal})
		}
		if match[2] >= 0 {
			part := ExpressionPart{
				Kind: "project_variable",
				Name: raw[match[2]:match[3]],
			}
			if match[4] >= 0 {
				part.Default = raw[match[4]:match[5]]
				part.HasDefault = true
			}
			parts = append(parts, part)
		} else {
			if !allowOutputs {
				return Expression{}, fmt.Errorf("service-output references are not allowed here")
			}
			collection := raw[match[6]:match[7]]
			service := raw[match[8]:match[9]]
			output := raw[match[10]:match[11]]
			outputs := outputCatalog[collection]
			sensitive, ok := outputs[output]
			if !ok {
				return Expression{}, fmt.Errorf("unknown %s output %q", strings.TrimSuffix(collection, "s"), output)
			}
			switch collection {
			case "databases":
				if _, ok := project.Databases[service]; !ok {
					return Expression{}, fmt.Errorf("references unknown database %q", service)
				}
			case "buckets":
				if _, ok := project.Buckets[service]; !ok {
					return Expression{}, fmt.Errorf("references unknown bucket %q", service)
				}
			}
			parts = append(parts, ExpressionPart{
				Kind:       "service_output",
				Collection: collection,
				Service:    service,
				Output:     output,
				Sensitive:  sensitive,
			})
		}
		cursor = match[1]
	}
	if cursor < len(raw) {
		literal := raw[cursor:]
		if containsExpressionMarker(literal) {
			return Expression{}, fmt.Errorf("contains a malformed variable or service-output expression")
		}
		parts = append(parts, ExpressionPart{Kind: "literal", Value: literal})
	}
	if len(parts) == 0 {
		if containsExpressionMarker(raw) {
			return Expression{}, fmt.Errorf("contains a malformed variable or service-output expression")
		}
		parts = []ExpressionPart{{Kind: "literal", Value: raw}}
	}
	return Expression{Parts: parts}, nil
}

func containsExpressionMarker(value string) bool {
	return strings.Contains(value, "${") || strings.Contains(value, "{{") || strings.Contains(value, "}}")
}

func ResolveExpression(expression Expression, values map[string]string) (string, error) {
	var result strings.Builder
	for _, part := range expression.Parts {
		switch part.Kind {
		case "literal":
			result.WriteString(part.Value)
		case "project_variable":
			value, ok := values[part.Name]
			if !ok || value == "" {
				if !part.HasDefault {
					return "", fmt.Errorf("missing project variable %s", part.Name)
				}
				value = part.Default
			}
			result.WriteString(value)
		case "service_output":
			return "", fmt.Errorf("service output %s.%s.%s is not a plain compile-time value", part.Collection, part.Service, part.Output)
		default:
			return "", fmt.Errorf("unknown expression part %q", part.Kind)
		}
	}
	return result.String(), nil
}

func expressionHasReference(expression Expression) bool {
	for _, part := range expression.Parts {
		if part.Kind != "literal" {
			return true
		}
	}
	return false
}
