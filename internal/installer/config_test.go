package installer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The transcript's node.yaml shapes: the fresh server and the joining
// agent (section 2 verbatim).
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

	agent, err := ParseNodeConfig([]byte(`role: agent
capabilities: [database]
join:
  server: https://cp-1.internal:6443
  tokenFile: /root/skali-join-token
`))
	require.NoError(t, err)
	require.Empty(t, agent.Cluster, "joining clusters are derived from current Skali tokens")
	require.NotNil(t, agent.Join)
	require.Equal(t, "https://cp-1.internal:6443", agent.Join.Server)
	require.Equal(t, "/root/skali-join-token", agent.Join.TokenFile)

	joiningServer, err := ParseNodeConfig([]byte(`role: server
capabilities: [application]
join:
  server: https://cp-1.internal:6443
  tokenFile: /root/skali-join-token
`))
	require.NoError(t, err)
	require.NotNil(t, joiningServer.Join)
	require.Equal(t, "server", joiningServer.Role)

	tokenDriven, err := ParseNodeConfig([]byte(`capabilities: [edge]
join:
  tokenFile: /root/skali-join-token
`))
	require.NoError(t, err)
	require.Empty(t, tokenDriven.Role)
	require.Empty(t, tokenDriven.Cluster)
	require.Empty(t, tokenDriven.Join.Server)

	pinned, err := ParseNodeConfig([]byte("role: server\ncapabilities: [edge]\nnodeIP: 192.168.64.5\n"))
	require.NoError(t, err)
	require.Equal(t, "192.168.64.5", pinned.NodeIP)
	require.Equal(t, "192.168.64.5", pinned.NodeNetwork().ClusterIP,
		"the deprecated alias still resolves to the cluster address")

	multiHomed, err := ParseNodeConfig([]byte(`role: server
capabilities: [edge]
network:
  clusterIP: 10.0.1.2
  publicIPs: [203.0.113.7]
  extraSANs: [cluster.example.com]
  coordinatorBind: [cluster, public]
`))
	require.NoError(t, err)
	require.Equal(t, NodeNetwork{
		ClusterIP: "10.0.1.2", PublicIPs: []string{"203.0.113.7"},
		ExtraSANs:       []string{"cluster.example.com"},
		CoordinatorBind: []string{"cluster", "public"},
	}, multiHomed.NodeNetwork())

	darwin, err := ParseNodeConfig([]byte(`role: server
capabilities: [application, database]
vm:
  name: skali-e2e-darwin
  network: user-v2
  cpus: 2
  memory: 3GiB
  disk: 15GiB
`))
	require.NoError(t, err)
	require.Equal(t, &VMConfig{
		Name: "skali-e2e-darwin", Network: "user-v2", CPUs: 2, Memory: "3GiB", Disk: "15GiB",
	}, darwin.VM)
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
		"agent without join": {"role: agent\ncapabilities: [database]\n",
			"role agent requires a join block"},
		"server join without token file": {"role: server\ncapabilities: [edge]\njoin:\n  server: https://cp-1.internal:6443\n",
			"join.tokenFile is required"},
		"join server not https": {"role: agent\ncapabilities: [database]\njoin:\n  server: http://cp-1.internal:6443\n  tokenFile: /root/token\n",
			"join server must be an https:// URL"},
		"join without token file": {"role: agent\ncapabilities: [database]\njoin:\n  server: https://cp-1.internal:6443\n",
			"join.tokenFile is required"},
		"bad node ip": {"role: server\ncapabilities: [edge]\nnodeIP: not-an-ip\n",
			"nodeIP \"not-an-ip\" is not a valid IP address"},
		"node ip disagrees with cluster ip": {
			"role: server\ncapabilities: [edge]\nnodeIP: 10.0.1.2\nnetwork:\n  clusterIP: 10.0.1.3\n",
			"keep only network.clusterIP"},
		"bad public ip": {"role: server\ncapabilities: [edge]\nnetwork:\n  publicIPs: [nope]\n",
			"public address \"nope\" is not a valid IP address"},
		"bad certificate name": {"role: server\ncapabilities: [edge]\nnetwork:\n  extraSANs: [\"not a name\"]\n",
			"neither an IP address nor a DNS name"},
		"public bind without public address": {
			"role: server\ncapabilities: [edge]\nnetwork:\n  coordinatorBind: [public]\n",
			"no public addresses are declared"},
		"multiple documents": {"role: server\ncapabilities: [edge]\n---\nrole: agent\ncapabilities: [edge]\n", "multiple YAML documents"},
		"bad vm network": {"role: server\ncapabilities: [edge]\nvm:\n  network: nat\n",
			"vm.network must be bridged, shared, or user-v2, got \"nat\""},
		"bad vm cpus": {"role: server\ncapabilities: [edge]\nvm:\n  cpus: -2\n",
			"vm.cpus must be a positive count"},
		"bad vm memory": {"role: server\ncapabilities: [edge]\nvm:\n  memory: 12GB\n",
			"vm.memory \"12GB\" is not a size like 12GiB"},
		"bad vm disk": {"role: server\ncapabilities: [edge]\nvm:\n  disk: lots\n",
			"vm.disk \"lots\" is not a size like 100GiB"},
		"bad vm name": {"role: server\ncapabilities: [edge]\nvm:\n  name: \"-oops\"\n",
			"vm.name \"-oops\" is not a valid Lima instance name"},
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
  registry: registry.example.com
tls:
  issuerEmail: ops@example.com
admin:
  email: nicholas@example.com
  passwordFile: /root/skali-admin-password
skalid:
  image: ghcr.io/hinkolas/skalid:v2.0.0
web:
  image: ghcr.io/hinkolas/skali-web:v2.0.0
`))
	require.NoError(t, err)
	require.Equal(t, "skali.example.com", config.Endpoints.API)
	require.Equal(t, "registry.example.com", config.Endpoints.Registry)
	require.Equal(t, "ops@example.com", config.TLS.IssuerEmail)
	require.Equal(t, "/root/skali-admin-password", config.Admin.PasswordFile)
	require.Equal(t, "ghcr.io/hinkolas/skalid:v2.0.0", config.Skalid.Image)
	require.Equal(t, "ghcr.io/hinkolas/skali-web:v2.0.0", config.Web.Image)
	require.Empty(t, config.StorageDriver(), "an omitted storage block keeps the recorded driver")
}

func TestParseInitConfigStorageDriver(t *testing.T) {
	t.Parallel()
	base := `endpoints:
  api: skali.example.com
  registry: registry.example.com
tls:
  issuerEmail: ops@example.com
admin:
  email: a@example.com
  passwordFile: /root/pw
skalid:
  image: skalid:dev
web:
  image: skali-web:dev
`
	config, err := ParseInitConfig([]byte(base + "storage:\n  driver: longhorn\n"))
	require.NoError(t, err)
	require.Equal(t, "longhorn", config.StorageDriver())

	config, err = ParseInitConfig([]byte(base + "storage:\n  driver: local\n"))
	require.NoError(t, err)
	require.Equal(t, "local", config.StorageDriver())

	_, err = ParseInitConfig([]byte(base + "storage:\n  driver: zfs\n"))
	require.ErrorContains(t, err, "storage.driver must be local or longhorn")
}

func TestParseInitConfigRejections(t *testing.T) {
	t.Parallel()
	base := map[string]string{
		"endpoints": "endpoints:\n  api: skali.example.com\n  registry: registry.example.com\n",
		"tls":       "tls:\n  issuerEmail: ops@example.com\n",
		"admin":     "admin:\n  email: a@example.com\n  passwordFile: /root/pw\n",
		"skalid":    "skalid:\n  image: skalid:dev\n",
		"web":       "web:\n  image: skali-web:dev\n",
	}
	build := func(omit string) string {
		var document strings.Builder
		for _, key := range []string{"endpoints", "tls", "admin", "skalid", "web"} {
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
		"web":       "web.image is required",
	}
	for omit, want := range cases {
		t.Run("missing "+omit, func(t *testing.T) {
			t.Parallel()
			_, err := ParseInitConfig([]byte(build(omit)))
			require.ErrorContains(t, err, want)
		})
	}

	t.Run("missing endpoints.registry", func(t *testing.T) {
		t.Parallel()
		document := "endpoints:\n  api: skali.example.com\n" + base["tls"] + base["admin"] + base["skalid"]
		_, err := ParseInitConfig([]byte(document))
		require.ErrorContains(t, err, "endpoints.registry is required")
	})

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
	require.Contains(t, string(node), `"user-v2"`)

	initSchema, err := InitConfigJSONSchema()
	require.NoError(t, err)
	require.Contains(t, string(initSchema), InitSchemaID)
	require.Contains(t, string(initSchema), "issuerEmail")
	require.Contains(t, string(initSchema), "managed-registry domain")
}
