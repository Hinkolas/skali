package installer

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/layout"
)

const validExistingConfig = `endpoints:
  api: skali.example.com
  registry: registry.example.com
tls:
  issuerEmail: ops@example.com
admin:
  email: admin@example.com
  passwordFile: /root/password
skalid:
  image: ghcr.io/hinkolas/skalid:v2.0.0
ingress:
  className: nginx
`

func TestParseExistingClusterConfig(t *testing.T) {
	t.Parallel()
	config, err := ParseExistingClusterConfig([]byte(validExistingConfig))
	require.NoError(t, err)
	require.Equal(t, DefaultCluster, config.Cluster)
	require.Equal(t, "nginx", config.Ingress.ClassName)
	require.Equal(t, string(layout.TierSingle), config.Database.Tier, "tier defaults to single")
	require.Equal(t, DefaultDatabaseStorage, config.Database.Storage)
	require.Equal(t, DefaultRegistryStorage, config.Registry.Storage)
	require.Equal(t, "install", config.Operators.CNPG)
	require.Equal(t, "install", config.Operators.CertManager)
	require.Equal(t, layout.RequiredCapabilities, config.Capabilities)
}

func TestParseExistingClusterConfigRejections(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		yaml string
		want string
	}{
		"unknown field": {validExistingConfig + "color: blue\n", "field color not found"},
		"missing ingress class": {
			"endpoints:\n  api: a.test\n  registry: r.test\ntls:\n  issuerEmail: o@test\n" +
				"admin:\n  email: a@test\n  passwordFile: /p\nskalid:\n  image: img\n",
			"ingress.className is required",
		},
		"bad tier": {
			validExistingConfig + "database:\n  tier: quorum\n",
			"database.tier must be single, asynchronous, or synchronous",
		},
		"bad operator": {
			validExistingConfig + "operators:\n  cnpg: maybe\n",
			"operators.cnpg must be install or use-existing",
		},
		"unknown capability": {
			validExistingConfig + "capabilities: [warp-drive]\n",
			`unknown capability "warp-drive"`,
		},
		"bad storage quantity": {
			validExistingConfig + "database:\n  storage: 10gigs\n",
			"database.storage",
		},
	}
	for name, tc := range cases {
		_, err := ParseExistingClusterConfig([]byte(tc.yaml))
		require.ErrorContains(t, err, tc.want, name)
	}
}

func TestParseExistingClusterConfigDedupesCapabilities(t *testing.T) {
	t.Parallel()
	config, err := ParseExistingClusterConfig([]byte(validExistingConfig +
		"capabilities: [application, application, database]\n"))
	require.NoError(t, err)
	require.Equal(t, []string{"application", "database"}, config.Capabilities)
}

func TestParseExistingClusterConfigUseExisting(t *testing.T) {
	t.Parallel()
	config, err := ParseExistingClusterConfig([]byte(validExistingConfig +
		"operators:\n  cnpg: use-existing\n  certManager: use-existing\n"))
	require.NoError(t, err)
	require.Equal(t, "use-existing", config.Operators.CNPG)
	require.Equal(t, "use-existing", config.Operators.CertManager)
}
