package localdev

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// dockerRemnants is what the fake docker lists: "<name> <k3d.role>"
// container lines and volume names under the k3d.cluster label filter, network names
// under app=k3d.
type dockerRemnants struct {
	containers, volumes, networks []string
	// fail names the docker command (its leading words) that exits 1.
	fail string
}

// fakeDeleteTools puts a fake k3d and docker in front of DeleteFor, both
// logging their invocations, one per line, to the returned file. k3d
// cluster delete exits with k3dExit and removes nothing, as against a
// partial cluster it does not see; docker answers its listings from
// remnants.
func fakeDeleteTools(t *testing.T, k3dExit int, remnants dockerRemnants) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	k3d := filepath.Join(bin, "k3d")
	require.NoError(t, os.WriteFile(k3d, []byte("#!/bin/sh\necho \"k3d $*\" >> '"+log+"'\nexit "+strconv.Itoa(k3dExit)+"\n"), 0o755))
	withPathK3d(t, k3d, nil)
	lines := func(items []string) string { return strings.Join(items, "\\n") }
	script := "#!/bin/sh\necho \"docker $*\" >> '" + log + "'\n"
	if remnants.fail != "" {
		script += "case \"$*\" in '" + remnants.fail + "'*) echo 'removal refused' >&2; exit 1;; esac\n"
	}
	script += "case \"$1 $2\" in\n" +
		"'ps --all') printf '" + lines(remnants.containers) + "';;\n" +
		"'volume ls') printf '" + lines(remnants.volumes) + "';;\n" +
		"'network ls') printf '" + lines(remnants.networks) + "';;\n" +
		"esac\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func deleteCalls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

const remnantCluster = "skali-dev-remnant-test"

var remnantListings = []string{
	"k3d cluster delete " + remnantCluster,
	"docker ps --all --filter label=k3d.cluster=" + remnantCluster + " --format {{.Names}} {{.Label \"k3d.role\"}}",
	"docker volume ls --quiet --filter label=k3d.cluster=" + remnantCluster,
	"docker network ls --filter label=app=k3d --format {{.Name}}",
}

// k3d cluster list omits a cluster whose only container left is the tools
// helper, yet k3d cluster create refuses its name; k3d's delete finds the
// helper but not the network, which only the server's labels name (#232).
func TestDeleteRemovesToolsHelperRemnant(t *testing.T) {
	log := fakeDeleteTools(t, 0, dockerRemnants{
		containers: []string{"k3d-" + remnantCluster + "-tools noRole"},
		networks:   []string{"k3d-" + remnantCluster, "k3d-skali-dev"},
	})
	require.NoError(t, DeleteFor(context.Background(), remnantCluster))
	require.Equal(t, []string{
		remnantListings[0], remnantListings[1],
		"docker rm --force --volumes k3d-" + remnantCluster + "-tools",
		remnantListings[2], remnantListings[3],
		"docker network rm k3d-" + remnantCluster,
	}, deleteCalls(t, log))
}

// A network or image volume alone is invisible to k3d entirely. Only this
// cluster's exact network goes, not one whose name merely extends it.
func TestDeleteRemovesNetworkAndVolumeRemnants(t *testing.T) {
	log := fakeDeleteTools(t, 0, dockerRemnants{
		volumes:  []string{"k3d-" + remnantCluster + "-images"},
		networks: []string{"k3d-" + remnantCluster + "-2", "k3d-" + remnantCluster, "k3d-skali-test"},
	})
	require.NoError(t, DeleteFor(context.Background(), remnantCluster))
	require.Equal(t, []string{
		remnantListings[0], remnantListings[1], remnantListings[2],
		"docker volume rm k3d-" + remnantCluster + "-images",
		remnantListings[3],
		"docker network rm k3d-" + remnantCluster,
	}, deleteCalls(t, log))
}

// k3d's delete keeps a registry another cluster still uses, though it
// carries this cluster's label; the sweep leaves that judgment to k3d.
// The registry still reserves the name, so the delete fails after
// removing everything else.
func TestDeleteKeepsRegistryK3dKept(t *testing.T) {
	log := fakeDeleteTools(t, 0, dockerRemnants{
		containers: []string{"k3d-shared-registry registry", "k3d-" + remnantCluster + "-tools noRole"},
		networks:   []string{"k3d-" + remnantCluster},
	})
	err := DeleteFor(context.Background(), remnantCluster)
	require.ErrorContains(t, err, "kept registry k3d-shared-registry")
	require.ErrorContains(t, err, "docker rm --force k3d-shared-registry")
	require.Equal(t, []string{
		remnantListings[0], remnantListings[1],
		"docker rm --force --volumes k3d-" + remnantCluster + "-tools",
		remnantListings[2], remnantListings[3],
		"docker network rm k3d-" + remnantCluster,
	}, deleteCalls(t, log))
}

func TestDeleteAbsentClusterRemovesNothing(t *testing.T) {
	log := fakeDeleteTools(t, 0, dockerRemnants{networks: []string{"k3d-skali-dev", "k3d-" + remnantCluster + "-2"}})
	require.NoError(t, DeleteFor(context.Background(), remnantCluster))
	require.Equal(t, remnantListings, deleteCalls(t, log))
}

func TestDeleteReportsCleanupFailures(t *testing.T) {
	t.Run("k3d", func(t *testing.T) {
		log := fakeDeleteTools(t, 1, dockerRemnants{containers: []string{"k3d-" + remnantCluster + "-tools noRole"}})
		require.ErrorContains(t, DeleteFor(context.Background(), remnantCluster), "k3d cluster delete")
		require.Equal(t, remnantListings[:1], deleteCalls(t, log), "nothing is swept past a failed k3d delete")
	})
	t.Run("listing", func(t *testing.T) {
		fakeDeleteTools(t, 0, dockerRemnants{fail: "ps"})
		err := DeleteFor(context.Background(), remnantCluster)
		require.ErrorContains(t, err, "docker ps --all")
		require.ErrorContains(t, err, "removal refused")
	})
	t.Run("removal", func(t *testing.T) {
		log := fakeDeleteTools(t, 0, dockerRemnants{
			volumes:  []string{"k3d-" + remnantCluster + "-images"},
			networks: []string{"k3d-" + remnantCluster},
			fail:     "volume rm",
		})
		err := DeleteFor(context.Background(), remnantCluster)
		require.ErrorContains(t, err, "docker volume rm k3d-"+remnantCluster+"-images")
		require.ErrorContains(t, err, "removal refused")
		require.NotContains(t, deleteCalls(t, log), "docker network rm k3d-"+remnantCluster)
	})
}
