package compiler

import (
	"fmt"
	"strings"

	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/naming"
)

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

// endpointBearing marks the outputs whose value embeds a network address:
// exactly these change when outputs are resolved for a non-internal
// audience (the resolved-environment API rewrites them to host-reachable
// addresses). Kept beside outputCatalog so the classification cannot drift
// from the catalog itself.
var endpointBearing = map[string]map[string]bool{
	"databases": {"host": true, "port": true, "url": true},
	"buckets":   {"endpoint": true},
}

// EndpointBearingOutput reports whether an output's value embeds a network
// address and therefore depends on the resolution audience.
func EndpointBearingOutput(collection, output string) bool {
	return endpointBearing[collection][output]
}

// parseExpression scans one raw manifest string into literal and reference
// parts. The grammar is flat: ${NAME} and ${NAME:-default} project values
// plus {{collection.key.output}} service outputs, interleaved with literal
// text. A lone $ or { passes through as literal text (shell syntax stays
// intact), but a started token must be well-formed and a stray "}}" is an
// error, so typos never pass through silently. Error positions are 1-based
// byte offsets into the value.
func parseExpression(raw string, project manifest.Project, allowOutputs bool) (Expression, error) {
	var parts []ExpressionPart
	var literal strings.Builder
	flush := func() {
		if literal.Len() > 0 {
			parts = append(parts, ExpressionPart{Kind: "literal", Value: literal.String()})
			literal.Reset()
		}
	}
	pos := 0
	for pos < len(raw) {
		switch {
		case strings.HasPrefix(raw[pos:], "${"):
			part, next, err := scanProjectVariable(raw, pos)
			if err != nil {
				return Expression{}, err
			}
			flush()
			parts = append(parts, part)
			pos = next
		case strings.HasPrefix(raw[pos:], "{{"):
			part, next, err := scanServiceOutput(raw, pos)
			if err != nil {
				return Expression{}, err
			}
			if !allowOutputs {
				return Expression{}, fmt.Errorf("service-output references are not allowed here")
			}
			part, err = checkServiceOutput(part, project)
			if err != nil {
				return Expression{}, err
			}
			flush()
			parts = append(parts, part)
			pos = next
		case strings.HasPrefix(raw[pos:], "}}"):
			return Expression{}, fmt.Errorf("stray \"}}\" at position %d has no matching \"{{\"", pos+1)
		default:
			literal.WriteByte(raw[pos])
			pos++
		}
	}
	flush()
	if len(parts) == 0 {
		parts = []ExpressionPart{{Kind: "literal", Value: raw}}
	}
	return Expression{Parts: parts}, nil
}

// ExpandVariables substitutes ${NAME} and ${NAME:-default} tokens using
// lookup and leaves all other text untouched, including {{...}} tokens
// and a lone $. It shares scanProjectVariable with the manifest grammar
// so the two ${...} surfaces can never drift. A lookup miss without a
// default is an error, so typos never pass through silently.
func ExpandVariables(raw string, lookup func(name string) (string, bool)) (string, error) {
	var expanded strings.Builder
	pos := 0
	for pos < len(raw) {
		if !strings.HasPrefix(raw[pos:], "${") {
			expanded.WriteByte(raw[pos])
			pos++
			continue
		}
		part, next, err := scanProjectVariable(raw, pos)
		if err != nil {
			return "", err
		}
		value, ok := lookup(part.Name)
		if !ok {
			if !part.HasDefault {
				return "", fmt.Errorf("unknown variable ${%s}", part.Name)
			}
			value = part.Default
		}
		expanded.WriteString(value)
		pos = next
	}
	return expanded.String(), nil
}

// scanProjectVariable scans one ${NAME} or ${NAME:-default} token whose "${"
// marker sits at start; next is the position after the closing brace. The
// default runs to the first "}" and may be empty; nesting is not supported.
func scanProjectVariable(raw string, start int) (part ExpressionPart, next int, err error) {
	pos := start + 2
	nameStart := pos
	if pos < len(raw) && isProjectNameStart(raw[pos]) {
		pos++
		for pos < len(raw) && isProjectNameChar(raw[pos]) {
			pos++
		}
	}
	name := raw[nameStart:pos]
	if name == "" {
		if pos == len(raw) {
			return part, 0, fmt.Errorf("unterminated ${...} expression at position %d", start+1)
		}
		return part, 0, fmt.Errorf("invalid ${...} expression at position %d: project value names match ^[A-Z_][A-Z0-9_]*$", start+1)
	}
	part = ExpressionPart{Kind: "project_variable", Name: name}
	switch {
	case pos < len(raw) && raw[pos] == '}':
		return part, pos + 1, nil
	case strings.HasPrefix(raw[pos:], ":-"):
		pos += 2
		defaultStart := pos
		for pos < len(raw) && raw[pos] != '}' {
			pos++
		}
		if pos == len(raw) {
			return part, 0, fmt.Errorf("unterminated ${...} expression at position %d", start+1)
		}
		part.Default = raw[defaultStart:pos]
		part.HasDefault = true
		return part, pos + 1, nil
	case pos == len(raw):
		return part, 0, fmt.Errorf("unterminated ${...} expression at position %d", start+1)
	default:
		return part, 0, fmt.Errorf("invalid ${...} expression at position %d: expected } or :- after the name", start+1)
	}
}

// scanServiceOutput scans one {{collection.key.output}} token whose "{{"
// marker sits at start; next is the position after the closing braces.
// Syntax only: catalog and declaration checks live in checkServiceOutput.
func scanServiceOutput(raw string, start int) (part ExpressionPart, next int, err error) {
	pos := skipSpaces(raw, start+2)
	collectionStart := pos
	for pos < len(raw) && isLowerLetter(raw[pos]) {
		pos++
	}
	collection := raw[collectionStart:pos]
	if collection != "databases" && collection != "buckets" {
		return part, 0, fmt.Errorf("invalid {{...}} expression at position %d: the collection must be databases or buckets", start+1)
	}
	if pos == len(raw) || raw[pos] != '.' {
		return part, 0, fmt.Errorf("invalid {{...}} expression at position %d: expected {{collection.key.output}}", start+1)
	}
	pos++
	keyStart := pos
	if pos < len(raw) && isLowerLetter(raw[pos]) {
		pos++
		for pos < len(raw) && isServiceKeyChar(raw[pos]) {
			pos++
		}
	}
	key := raw[keyStart:pos]
	if key == "" || len(key) > 63 {
		return part, 0, fmt.Errorf("invalid service key in {{...}} at position %d: keys match %s", start+1, naming.KeyPattern)
	}
	if pos == len(raw) || raw[pos] != '.' {
		return part, 0, fmt.Errorf("invalid {{...}} expression at position %d: expected {{collection.key.output}}", start+1)
	}
	pos++
	outputStart := pos
	if pos < len(raw) && isOutputNameStart(raw[pos]) {
		pos++
		for pos < len(raw) && isOutputNameChar(raw[pos]) {
			pos++
		}
	}
	output := raw[outputStart:pos]
	if output == "" {
		return part, 0, fmt.Errorf("invalid service output in {{...}} at position %d: outputs match ^[a-z_][a-z0-9_]*$", start+1)
	}
	pos = skipSpaces(raw, pos)
	if pos == len(raw) {
		return part, 0, fmt.Errorf("unterminated {{...}} expression at position %d", start+1)
	}
	if !strings.HasPrefix(raw[pos:], "}}") {
		return part, 0, fmt.Errorf("invalid {{...}} expression at position %d: expected }}", start+1)
	}
	return ExpressionPart{
		Kind:       "service_output",
		Collection: collection,
		Service:    key,
		Output:     output,
	}, pos + 2, nil
}

// checkServiceOutput resolves a scanned output token against the catalog
// and the project's declared services, stamping the sensitivity flag.
func checkServiceOutput(part ExpressionPart, project manifest.Project) (ExpressionPart, error) {
	sensitive, ok := outputCatalog[part.Collection][part.Output]
	if !ok {
		return part, fmt.Errorf("unknown %s output %q", strings.TrimSuffix(part.Collection, "s"), part.Output)
	}
	part.Sensitive = sensitive
	switch part.Collection {
	case "databases":
		if _, ok := project.Databases[part.Service]; !ok {
			return part, fmt.Errorf("references unknown database %q", part.Service)
		}
	case "buckets":
		if _, ok := project.Buckets[part.Service]; !ok {
			return part, fmt.Errorf("references unknown bucket %q", part.Service)
		}
	}
	return part, nil
}

func isProjectNameStart(c byte) bool { return c == '_' || c >= 'A' && c <= 'Z' }
func isProjectNameChar(c byte) bool  { return isProjectNameStart(c) || c >= '0' && c <= '9' }
func isLowerLetter(c byte) bool      { return c >= 'a' && c <= 'z' }
func isServiceKeyChar(c byte) bool   { return c == '-' || isLowerLetter(c) || c >= '0' && c <= '9' }
func isOutputNameStart(c byte) bool  { return c == '_' || isLowerLetter(c) }
func isOutputNameChar(c byte) bool   { return isOutputNameStart(c) || c >= '0' && c <= '9' }

// skipSpaces advances past the whitespace {{...}} tolerates around its body.
func skipSpaces(raw string, pos int) int {
	for pos < len(raw) {
		switch raw[pos] {
		case ' ', '\t', '\n', '\f', '\r':
			pos++
		default:
			return pos
		}
	}
	return pos
}

// ServiceOutput returns the sole part when the expression is exactly one
// {{collection.service.output}} reference.
func (e Expression) ServiceOutput() (ExpressionPart, bool) {
	if len(e.Parts) == 1 && e.Parts[0].Kind == "service_output" {
		return e.Parts[0], true
	}
	return ExpressionPart{}, false
}

// HasProjectVariables reports whether any part references a ${NAME} value.
func (e Expression) HasProjectVariables() bool {
	for _, part := range e.Parts {
		if part.Kind == "project_variable" {
			return true
		}
	}
	return false
}

// Literal returns the concatenated literal text of a pure-literal
// expression. Reference parts contribute nothing.
func (e Expression) Literal() string {
	var result strings.Builder
	for _, part := range e.Parts {
		if part.Kind == "literal" {
			result.WriteString(part.Value)
		}
	}
	return result.String()
}

func hasServiceOutputPart(e Expression) bool {
	for _, part := range e.Parts {
		if part.Kind == "service_output" {
			return true
		}
	}
	return false
}

func ResolveExpression(expression Expression, values map[string]string) (string, error) {
	var result strings.Builder
	for _, part := range expression.Parts {
		if part.Kind == "service_output" {
			return "", fmt.Errorf("service output %s.%s.%s is not a plain compile-time value", part.Collection, part.Service, part.Output)
		}
		value, err := resolvePlainPart(part, values)
		if err != nil {
			return "", err
		}
		result.WriteString(value)
	}
	return result.String(), nil
}

// ResolveExpressionOutputs resolves like ResolveExpression but additionally
// substitutes {{collection.service.output}} references from the outputs map,
// keyed by "<collection>.<service>" and then by output name. It backs the
// resolved-environment API, the one consumer that materializes service
// outputs outside the cluster; the in-cluster path binds outputs by Secret
// reference instead and never sees their values.
func ResolveExpressionOutputs(expression Expression, values map[string]string, outputs map[string]map[string]string) (string, error) {
	var result strings.Builder
	for _, part := range expression.Parts {
		if part.Kind == "service_output" {
			set, ok := outputs[part.Collection+"."+part.Service]
			if !ok {
				return "", fmt.Errorf("missing outputs for %s.%s", part.Collection, part.Service)
			}
			value, ok := set[part.Output]
			if !ok {
				return "", fmt.Errorf("missing output %s.%s.%s", part.Collection, part.Service, part.Output)
			}
			result.WriteString(value)
			continue
		}
		value, err := resolvePlainPart(part, values)
		if err != nil {
			return "", err
		}
		result.WriteString(value)
	}
	return result.String(), nil
}

// resolvePlainPart resolves the literal and project-variable arms shared by
// ResolveExpression and ResolveExpressionOutputs.
func resolvePlainPart(part ExpressionPart, values map[string]string) (string, error) {
	switch part.Kind {
	case "literal":
		return part.Value, nil
	case "project_variable":
		// A present empty string is a real value; only absence falls back
		// to the inline default.
		value, ok := values[part.Name]
		if !ok {
			if !part.HasDefault {
				return "", fmt.Errorf("missing project variable %s", part.Name)
			}
			value = part.Default
		}
		return value, nil
	default:
		return "", fmt.Errorf("unknown expression part %q", part.Kind)
	}
}
