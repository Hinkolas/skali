package manifest

//go:generate go run ../../cmd/skali-schema --output ../../schemas/skali.schema.json

import (
	"reflect"

	"github.com/Hinkolas/skali/internal/naming"
	"github.com/Hinkolas/skali/internal/utils"
	"github.com/Hinkolas/skali/internal/yamldoc"
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
		{Type: "string", Const: new(any("all"))},
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

	yamldoc.StampSchema(schema, SchemaID, "Skali project manifest",
		"Portable, declarative project definition consumed by the Skali compiler.")
	schema.Properties["version"].Const = new(any(CurrentVersion))
	schema.Properties["name"].Pattern = naming.KeyPattern
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
	application.Properties["build"].Properties["context"].MinLength = new(1)
	application.Properties["build"].Required = []string{"context"}
	application.Properties["platforms"].Items = &jsonschema.Schema{
		Type: "string", Enum: utils.AnySlice(PlatformAMD64, PlatformARM64),
	}
	application.Properties["platforms"].UniqueItems = true
	application.Properties["ports"].PropertyNames = stableKeySchema()
	application.Properties["routes"].PropertyNames = stableKeySchema()
	application.Properties["volumes"].PropertyNames = stableKeySchema()
	application.Properties["commands"].PropertyNames = stableKeySchema()
	application.Properties["commands"].AdditionalProperties.MinItems = new(1)
	dev := application.Properties["dev"]
	dev.Properties["command"].MinItems = new(1)
	dev.Properties["ports"].PropertyNames = stableKeySchema()
	dev.Properties["ports"].AdditionalProperties.Minimum = new(float64(1))
	dev.Properties["ports"].AdditionalProperties.Maximum = new(float64(65535))

	port := application.Properties["ports"].AdditionalProperties
	port.Properties["port"].Minimum = new(float64(1))
	port.Properties["port"].Maximum = new(float64(65535))
	port.Properties["protocol"].Enum = utils.AnySlice("http", "https", "tcp", "udp")

	route := application.Properties["routes"].AdditionalProperties
	route.Properties["domain"].MinLength = new(1)
	route.Properties["path"].Pattern = "^/"
	route.Properties["port"] = portTargetSchema()
	route.Properties["tls"].Enum = utils.AnySlice("automatic", "optional", "disabled")
	route.Properties["strategy"].Enum = utils.AnySlice("round-robin", "least-requests")

	resourceValues := application.Properties["resources"].Properties["requests"]
	resourceValues.Properties["cpu"] = cpuSchema()
	resourceValues.Properties["memory"] = quantitySchema()
	resourceValues.Properties["temporaryStorage"] = quantitySchema()
	resourceLimits := application.Properties["resources"].Properties["limits"]
	resourceLimits.Properties["cpu"] = cpuSchema()
	resourceLimits.Properties["memory"] = quantitySchema()
	resourceLimits.Properties["temporaryStorage"] = quantitySchema()

	scaling := application.Properties["scaling"]
	scaling.Properties["replicas"].Properties["min"].Minimum = new(float64(1))
	scaling.Properties["replicas"].Properties["max"].Minimum = new(float64(1))
	scaling.Properties["autoscaling"].Properties["cpu"].Properties["targetUtilization"].Minimum = new(float64(1))
	scaling.Properties["autoscaling"].Properties["cpu"].Properties["targetUtilization"].Maximum = new(float64(100))

	application.Properties["placement"].Properties["spread"].Properties["across"].Enum = utils.AnySlice("nodes", "zones")
	application.Properties["placement"].Properties["spread"].Properties["enforcement"].Enum = utils.AnySlice("preferred", "required")
	rollout := application.Properties["deployment"].Properties["rollout"]
	rollout.Properties["strategy"].Enum = utils.AnySlice("blue-green", "rolling", "recreate")
	rollout.Properties["maxUnavailable"].Minimum = new(float64(0))
	rollout.Properties["maxSurge"].Minimum = new(float64(0))
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
	database.Properties["engine"].Enum = utils.AnySlice("postgres")
	database.Properties["version"] = versionSchema()
	database.Properties["isolation"].Enum = utils.AnySlice("shared", "project", "dedicated")
	database.Properties["availability"].Enum = utils.AnySlice("single", "asynchronous", "synchronous")
	database.Properties["storage"].Properties["size"] = quantitySchema()
	setDuration(database.Properties["recovery"], "pointInTime")

	bucket := schema.Properties["buckets"].AdditionalProperties
	bucket.Properties["visibility"].Enum = utils.AnySlice("private", "public-read")
	bucket.Properties["versioning"].Enum = utils.AnySlice("enabled", "disabled")
	bucket.Properties["quotas"].Properties["storage"] = quantitySchema()
	bucket.Properties["quotas"].Properties["maxObjectSize"] = quantitySchema()
	setDuration(bucket.Properties["lifecycle"], "abortIncompleteUploadsAfter")
	setDuration(bucket.Properties["lifecycle"], "expireNoncurrentVersionsAfter")

	backup := schema.Properties["backups"].AdditionalProperties
	backup.Properties["schedule"].MinLength = new(1)
	backup.Properties["retention"] = durationSchema()

	return schema, nil
}

func JSONSchema() ([]byte, error) {
	return yamldoc.MarshalSchema(Schema)
}

func setDuration(schema *jsonschema.Schema, property string) {
	schema.Properties[property] = durationSchema()
}

func stableKeySchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Pattern: naming.KeyPattern}
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
		{Type: "number", ExclusiveMinimum: new(float64(0))},
		{Type: "string", Pattern: "^[0-9]+(?:\\.[0-9]+)?$"},
	}}
}

func versionSchema() *jsonschema.Schema {
	return &jsonschema.Schema{AnyOf: []*jsonschema.Schema{
		{Type: "integer", Minimum: new(float64(1))},
		{Type: "string", Pattern: "^[1-9][0-9]*$"},
	}}
}

func portTargetSchema() *jsonschema.Schema {
	return &jsonschema.Schema{AnyOf: []*jsonschema.Schema{
		{Type: "integer", Minimum: new(float64(1)), Maximum: new(float64(65535))},
		stableKeySchema(),
	}}
}
