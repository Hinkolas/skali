package manifest

//go:generate go run ../../cmd/skali-schema --output ../../schemas/skali.schema.json

import (
	"encoding/json"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
)

const SchemaID = "https://skali.dev/schemas/v1/skali.schema.json"

func Schema() (*jsonschema.Schema, error) {
	textSchema := &jsonschema.Schema{AnyOf: []*jsonschema.Schema{
		{Type: "string"},
		{Type: "number"},
	}}
	scalarSchema := &jsonschema.Schema{AnyOf: []*jsonschema.Schema{
		{Type: "string"},
		{Type: "number"},
		{Type: "boolean"},
	}}
	selectionSchema := &jsonschema.Schema{OneOf: []*jsonschema.Schema{
		{Type: "string", Const: anyPointer("all")},
		{Type: "array", Items: stableKeySchema(), UniqueItems: true},
	}}

	schema, err := jsonschema.For[Project](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[Text]():      textSchema,
			reflect.TypeFor[Scalar]():    scalarSchema,
			reflect.TypeFor[Selection](): selectionSchema,
		},
	})
	if err != nil {
		return nil, err
	}

	schema.ID = SchemaID
	schema.Schema = "https://json-schema.org/draft/2020-12/schema"
	schema.Title = "Skali project manifest"
	schema.Description = "Portable, declarative project definition consumed by the Skali compiler."
	schema.Properties["version"].Const = anyPointer(CurrentVersion)
	schema.Properties["name"].Pattern = stableKeyPattern.String()
	schema.AnyOf = []*jsonschema.Schema{
		{Required: []string{"applications"}},
		{Required: []string{"databases"}},
		{Required: []string{"buckets"}},
	}

	for _, collection := range []string{"applications", "databases", "buckets", "backups"} {
		schema.Properties[collection].PropertyNames = stableKeySchema()
	}

	application := schema.Properties["applications"].AdditionalProperties
	application.AllOf = append(application.AllOf, &jsonschema.Schema{OneOf: []*jsonschema.Schema{
		{Required: []string{"image"}, Not: &jsonschema.Schema{Required: []string{"build"}}},
		{Required: []string{"build"}, Not: &jsonschema.Schema{Required: []string{"image"}}},
	}})
	application.Properties["build"].Properties["context"].MinLength = intPointer(1)
	application.Properties["build"].Required = []string{"context"}
	application.Properties["ports"].PropertyNames = stableKeySchema()
	application.Properties["routes"].PropertyNames = stableKeySchema()
	application.Properties["volumes"].PropertyNames = stableKeySchema()

	port := application.Properties["ports"].AdditionalProperties
	port.Properties["port"].Minimum = floatPointer(1)
	port.Properties["port"].Maximum = floatPointer(65535)
	port.Properties["protocol"].Enum = enum("http", "https", "tcp", "udp")

	route := application.Properties["routes"].AdditionalProperties
	route.Properties["domain"].MinLength = intPointer(1)
	route.Properties["path"].Pattern = "^/"
	route.Properties["port"] = portTargetSchema()
	route.Properties["tls"].Enum = enum("automatic", "disabled")

	resourceValues := application.Properties["resources"].Properties["requests"]
	resourceValues.Properties["cpu"] = cpuSchema()
	resourceValues.Properties["memory"] = quantitySchema()
	resourceValues.Properties["temporaryStorage"] = quantitySchema()
	resourceLimits := application.Properties["resources"].Properties["limits"]
	resourceLimits.Properties["cpu"] = cpuSchema()
	resourceLimits.Properties["memory"] = quantitySchema()
	resourceLimits.Properties["temporaryStorage"] = quantitySchema()

	scaling := application.Properties["scaling"]
	scaling.Properties["replicas"].Properties["min"].Minimum = floatPointer(1)
	scaling.Properties["replicas"].Properties["max"].Minimum = floatPointer(1)
	scaling.Properties["autoscaling"].Properties["cpu"].Properties["targetUtilization"].Minimum = floatPointer(1)
	scaling.Properties["autoscaling"].Properties["cpu"].Properties["targetUtilization"].Maximum = floatPointer(100)

	application.Properties["placement"].Properties["spread"].Properties["across"].Enum = enum("nodes", "zones")
	application.Properties["placement"].Properties["spread"].Properties["enforcement"].Enum = enum("preferred", "required")
	application.Properties["deployment"].Properties["rollout"].Properties["strategy"].Enum = enum("rolling", "recreate")
	setDuration(application.Properties["deployment"].Properties["releaseCommand"], "timeout")
	setDuration(application.Properties["deployment"].Properties["rollout"], "timeout")
	setDuration(application.Properties["shutdown"], "gracePeriod")
	for _, probeName := range []string{"startup", "readiness", "liveness"} {
		probe := application.Properties["health"].Properties[probeName]
		setDuration(probe, "interval")
		setDuration(probe, "timeout")
	}
	for _, volume := range []string{"size"} {
		application.Properties["volumes"].AdditionalProperties.Properties[volume] = quantitySchema()
	}

	database := schema.Properties["databases"].AdditionalProperties
	database.Properties["engine"].Enum = enum("postgres")
	database.Properties["version"] = versionSchema()
	database.Properties["isolation"].Enum = enum("shared", "project", "dedicated")
	database.Properties["availability"].Enum = enum("single", "asynchronous", "synchronous")
	database.Properties["storage"].Properties["size"] = quantitySchema()
	setDuration(database.Properties["recovery"], "pointInTime")

	bucket := schema.Properties["buckets"].AdditionalProperties
	bucket.Properties["visibility"].Enum = enum("private", "public-read")
	bucket.Properties["versioning"].Enum = enum("enabled", "disabled")
	bucket.Properties["quotas"].Properties["storage"] = quantitySchema()
	bucket.Properties["quotas"].Properties["maxObjectSize"] = quantitySchema()
	setDuration(bucket.Properties["lifecycle"], "abortIncompleteUploadsAfter")
	setDuration(bucket.Properties["lifecycle"], "expireNoncurrentVersionsAfter")

	backup := schema.Properties["backups"].AdditionalProperties
	backup.Properties["schedule"].MinLength = intPointer(1)
	backup.Properties["retention"] = durationSchema()

	return schema, nil
}

func JSONSchema() ([]byte, error) {
	schema, err := Schema()
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func setDuration(schema *jsonschema.Schema, property string) {
	schema.Properties[property] = durationSchema()
}

func stableKeySchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Pattern: stableKeyPattern.String()}
}

func quantitySchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:     "string",
		Pattern:  "^[0-9]+(?:\\.[0-9]+)?(?:B|KB|MB|GB|TB|KiB|MiB|GiB|TiB)$",
		Examples: []any{"256MB", "1GB", "512MiB", "2GiB"},
	}
}

func durationSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:     "string",
		Pattern:  "^[0-9]+(?:\\.[0-9]+)?(?:ms|s|m|h|d|w)$",
		Examples: []any{"30s", "5m", "7d"},
	}
}

func cpuSchema() *jsonschema.Schema {
	return &jsonschema.Schema{AnyOf: []*jsonschema.Schema{
		{Type: "number", ExclusiveMinimum: floatPointer(0)},
		{Type: "string", Pattern: "^[0-9]+(?:\\.[0-9]+)?$"},
	}}
}

func versionSchema() *jsonschema.Schema {
	return &jsonschema.Schema{AnyOf: []*jsonschema.Schema{
		{Type: "integer", Minimum: floatPointer(1)},
		{Type: "string", Pattern: "^[1-9][0-9]*$"},
	}}
}

func portTargetSchema() *jsonschema.Schema {
	return &jsonschema.Schema{AnyOf: []*jsonschema.Schema{
		{Type: "integer", Minimum: floatPointer(1), Maximum: floatPointer(65535)},
		stableKeySchema(),
	}}
}

func enum(values ...string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func anyPointer(value any) *any           { return &value }
func floatPointer(value float64) *float64 { return &value }
func intPointer(value int) *int           { return &value }
