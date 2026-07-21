package installer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The transcript's node.yaml, minus the join block this slice refuses.
func TestParseNodeConfig(t *testing.T) {
	t.Parallel()
	config, err := ParseNodeConfig([]byte(`role: server
capabilities: [application, database, object-storage, registry, edge]
`))
	require.NoError(t, err)
	require.Equal(t, DefaultCluster, config.Cluster)
	require.Equal(t, "server", config.Role)
	require.Len(t, config.Capabilities, 5)

	named, err := ParseNodeConfig([]byte("cluster: e2e\nrole: server\ncapabilities: [application]\n"))
	require.NoError(t, err)
	require.Equal(t, "e2e", named.Cluster)
}

func TestParseNodeConfigRejections(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		yaml string
		want string
	}{
		"unknown field":      {"role: server\ncapabilities: [edge]\ncolor: blue\n", "field color not found"},
		"bad role":           {"role: primary\ncapabilities: [edge]\n", "role must be server or agent"},
		"no capabilities":    {"role: server\ncapabilities: []\n", "at least one capability"},
		"unknown capability": {"role: server\ncapabilities: [gpu]\n", "unknown capability"},
		"join refused": {"role: agent\ncapabilities: [database]\njoin:\n  server: https://cp-1.internal:6443\n  tokenFile: /root/token\n",
			"join is not implemented in this slice"},
		"multiple documents": {"role: server\ncapabilities: [edge]\n---\nrole: agent\ncapabilities: [edge]\n", "multiple YAML documents"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseNodeConfig([]byte(testCase.yaml))
			require.ErrorContains(t, err, testCase.want)
		})
	}
}

// The transcript's init.yaml, extended with the image this slice requires.
func TestParseInitConfig(t *testing.T) {
	t.Parallel()
	config, err := ParseInitConfig([]byte(`endpoints:
  api: skali.example.com
tls:
  issuerEmail: ops@example.com
admin:
  email: nicholas@example.com
  passwordFile: /root/skali-admin-password
skalid:
  image: ghcr.io/hinkolas/skalid:v2.0.0
`))
	require.NoError(t, err)
	require.Equal(t, "skali.example.com", config.Endpoints.API)
	require.Equal(t, "ops@example.com", config.TLS.IssuerEmail)
	require.Equal(t, "/root/skali-admin-password", config.Admin.PasswordFile)
	require.Equal(t, "ghcr.io/hinkolas/skalid:v2.0.0", config.Skalid.Image)
}

func TestParseInitConfigRejections(t *testing.T) {
	t.Parallel()
	base := map[string]string{
		"endpoints": "endpoints:\n  api: skali.example.com\n",
		"tls":       "tls:\n  issuerEmail: ops@example.com\n",
		"admin":     "admin:\n  email: a@example.com\n  passwordFile: /root/pw\n",
		"skalid":    "skalid:\n  image: skalid:dev\n",
	}
	build := func(omit string) string {
		var document strings.Builder
		for _, key := range []string{"endpoints", "tls", "admin", "skalid"} {
			if key != omit {
				document.WriteString(base[key])
			}
		}
		return document.String()
	}
	cases := map[string]string{
		"endpoints": "endpoints.api is required",
		"tls":       "tls.issuerEmail is required",
		"admin":     "admin.email is required",
		"skalid":    "skalid.image is required",
	}
	for omit, want := range cases {
		t.Run("missing "+omit, func(t *testing.T) {
			t.Parallel()
			_, err := ParseInitConfig([]byte(build(omit)))
			require.ErrorContains(t, err, want)
		})
	}

	_, err := ParseInitConfig([]byte(build("") + "extra: field\n"))
	require.ErrorContains(t, err, "field extra not found")
}

// The generated schemas stay in sync with the wire types.
func TestConfigSchemas(t *testing.T) {
	t.Parallel()
	node, err := NodeConfigJSONSchema()
	require.NoError(t, err)
	require.Contains(t, string(node), NodeSchemaID)
	require.Contains(t, string(node), `"server"`)

	initSchema, err := InitConfigJSONSchema()
	require.NoError(t, err)
	require.Contains(t, string(initSchema), InitSchemaID)
	require.Contains(t, string(initSchema), "issuerEmail")
}
