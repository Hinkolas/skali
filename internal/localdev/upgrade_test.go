package localdev

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/version"
)

// Releases move forward only; working-tree platforms have no release to
// move between.
func TestReleaseTransitions(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cases := []struct {
		installed, target string
		refusal           string // empty when skali dev upgrade takes it
	}{
		{"v0.4.0", "v0.4.0", ""},
		{"v0.4.0", "v0.5.0", ""},
		{"v0.4.0-rc.2", "v0.4.0", ""},
		{"v0.4.0", "v0.6.0-beta.1", ""},
		{"v0.5.0", "v0.4.0", "newer than this CLI's v0.4.0"},
		{"v0.4.0", "v0.4.0-rc.9", "newer than this CLI's v0.4.0-rc.9"},
		{"", "v0.4.0", "working-tree platforms have no release"},
		{"v0.4.0", "v0.0.0-dev", "working-tree platforms have no release"},
		{"", "v0.0.0-dev", ""},
	}
	for _, c := range cases {
		withVersion(t, c.target)
		state := &State{Version: c.installed, K3sImage: PlatformK3sImage()}
		err := checkTransition(state)
		if c.refusal == "" {
			require.NoError(t, err, "%q -> %q", c.installed, c.target)
			continue
		}
		require.ErrorContains(t, err, c.refusal, "%q -> %q", c.installed, c.target)
		require.ErrorContains(t, err, "skali dev reset", "a refusal names the destructive way out")
	}
}

// k3s moves like Kubernetes does: patches and revisions freely, minors one
// at a time, never back.
func TestK3sTransitions(t *testing.T) {
	cases := []struct {
		installed, target string
		refusal           string
	}{
		{"rancher/k3s:v1.36.3-k3s1", "rancher/k3s:v1.36.3-k3s1", ""},
		{"rancher/k3s:v1.36.3-k3s1", "rancher/k3s:v1.36.3-k3s2", ""},
		{"rancher/k3s:v1.36.3-k3s1", "rancher/k3s:v1.36.9-k3s1", ""},
		{"rancher/k3s:v1.35.4-k3s1", "rancher/k3s:v1.36.3-k3s1", ""},
		{"rancher/k3s:v1.34.4-k3s1", "rancher/k3s:v1.36.3-k3s1", "pins k3s v1.35"},
		{"rancher/k3s:v1.36.3-k3s1", "rancher/k3s:v1.35.9-k3s1", "cannot be downgraded"},
		{"rancher/k3s:v1.36.3-k3s2", "rancher/k3s:v1.36.3-k3s1", "cannot be downgraded"},
		{"rancher/k3s:v1.36.3-k3s1", "rancher/k3s:v2.0.0-k3s1", "one minor version at a time"},
		{"old", "rancher/k3s:v1.36.3-k3s1", "not two releases of one k3s"},
		{"example.com/k3s:v1.36.3-k3s1", "rancher/k3s:v1.36.3-k3s1", "not two releases of one k3s"},
	}
	for _, c := range cases {
		err := checkK3sTransition(c.installed, c.target)
		if c.refusal == "" {
			require.NoError(t, err, "%s -> %s", c.installed, c.target)
			continue
		}
		require.ErrorContains(t, err, c.refusal, "%s -> %s", c.installed, c.target)
	}
}

// The ordinary commands name skali dev upgrade for a supported move, and
// refuse a platform whose upgrade did not finish until it is finished.
func TestCheckVersionNamesUpgrade(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	withVersion(t, "v0.4.0")
	state, err := NewState(version.PublishedSkalidImage("v0.4.0"))
	require.NoError(t, err)
	withVersion(t, "v0.5.0")
	err = CheckVersion(state)
	require.ErrorContains(t, err, "runs v0.4.0")
	require.ErrorContains(t, err, "requires v0.5.0")
	require.ErrorContains(t, err, "run skali dev upgrade")

	t.Setenv("SKALI_DEV_K3S_IMAGE", "rancher/k3s:v1.37.0-k3s1")
	withVersion(t, "v0.4.0")
	require.ErrorContains(t, CheckVersion(state), "run skali dev upgrade")
	t.Setenv("SKALI_DEV_K3S_IMAGE", "rancher/k3s:v1.38.0-k3s1")
	require.ErrorContains(t, CheckVersion(state), "one minor version at a time")
	t.Setenv("SKALI_DEV_K3S_IMAGE", "")

	// Mid-upgrade the record already names the target release, so the
	// target's own CLI refuses it too, until skali dev upgrade finishes.
	withVersion(t, "v0.5.0")
	state.Version = "v0.5.0"
	state.SkalidImage = version.PublishedSkalidImage("v0.5.0")
	state.Upgrade = &UpgradeRecord{FromVersion: "v0.4.0", ToVersion: "v0.5.0", StartedAt: time.Now()}
	err = CheckVersion(state)
	require.ErrorContains(t, err, "upgrade from v0.4.0 to v0.5.0 did not finish")
	require.ErrorContains(t, err, "run skali dev upgrade with skali v0.5.0")
	require.NoError(t, checkUpgradable(state), "the upgrade itself continues it")

	// The record survives a round trip: an older CLI that does not know
	// the field still sees the target release and refuses the platform.
	require.NoError(t, SaveState(state))
	loaded, err := LoadState()
	require.NoError(t, err)
	require.Equal(t, state.Upgrade.FromVersion, loaded.Upgrade.FromVersion)
	withVersion(t, "v0.4.0")
	require.Error(t, CheckVersion(&State{Version: loaded.Version, K3sImage: loaded.K3sImage,
		SkalidImage: loaded.SkalidImage, Edge: loaded.Edge}))
}

func TestComponentChanges(t *testing.T) {
	installed := []string{
		"rancher/mirrored-library-traefik:3.7.8",
		"ghcr.io/cloudnative-pg/cloudnative-pg:1.29.2",
		"ghcr.io/cloudnative-pg/postgresql:18.4-system-trixie",
		"ghcr.io/cloudnative-pg/postgresql:17.9-system-trixie",
		"registry:2.8.3",
		"localhost:5000/tool:1",
	}
	required := []string{
		"rancher/mirrored-library-traefik:3.8.0",
		"ghcr.io/cloudnative-pg/cloudnative-pg:1.29.2",
		"ghcr.io/cloudnative-pg/postgresql:18.4-system-trixie",
		"ghcr.io/cloudnative-pg/postgresql:17.10-system-trixie",
		"registry:2.8.3",
		"chrislusf/seaweedfs:4.40",
	}
	require.Equal(t, []ComponentChange{
		{Repository: "rancher/mirrored-library-traefik", From: "3.7.8", To: "3.8.0"},
		{Repository: "ghcr.io/cloudnative-pg/postgresql", From: "17.9-system-trixie", To: "17.10-system-trixie"},
		{Repository: "chrislusf/seaweedfs", To: "4.40"},
	}, componentChanges(installed, required))
	require.Empty(t, componentChanges(required, required))

	repository, tag := splitImage("localhost:5000/tool")
	require.Equal(t, "localhost:5000/tool", repository)
	require.Empty(t, tag)
}

func TestLatestMigrationIsEmbedded(t *testing.T) {
	require.Greater(t, latestMigration(), 0)
}

// Node fixtures mirror a k3d 5.9 node renamed by Create and pinned by
// pinNodeIP, inspected on Docker 29.
const nodeImageFixture = `[{"Config":{
	"Env":["PATH=/var/lib/rancher/k3s/data/cni:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/bin/aux","CRI_CONFIG_FILE=/var/lib/rancher/k3s/agent/etc/crictl.yaml"],
	"Cmd":["agent"],"Entrypoint":["/bin/k3s"],
	"Labels":{"org.opencontainers.image.version":"v1.36.3-k3s1","org.opencontainers.image.title":"k3s"}}}]`

const nodeContainerFixture = `[{"Id":"a5dc668700c8f00","Image":"sha256:old","Config":{
	"Hostname":"k3d-skali-dev-server-0","Image":"rancher/k3s:v1.36.3-k3s1",
	"Env":["K3S_TOKEN=secret","K3S_KUBECONFIG_OUTPUT=/output/kubeconfig.yaml","PATH=/var/lib/rancher/k3s/data/cni:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/bin/aux","CRI_CONFIG_FILE=/var/lib/rancher/k3s/agent/etc/crictl.yaml"],
	"Cmd":["server","--tls-san","0.0.0.0"],"Entrypoint":["/bin/k3d-entrypoint.sh"],
	"Labels":{"app":"k3d","k3d.cluster":"skali-dev","k3d.role":"server","k3d.server.api.port":"64713",
		"org.opencontainers.image.version":"v1.36.3-k3s1","org.opencontainers.image.title":"k3s"}},
	"HostConfig":{"Privileged":true,"Init":true,"RestartPolicy":{"Name":"unless-stopped","MaximumRetryCount":0},
		"Tmpfs":{"/run":"","/var/run":""},"SecurityOpt":["label=disable"],"CgroupnsMode":"private","ExtraHosts":null,
		"PortBindings":{"443/tcp":[{"HostIp":"127.0.0.1","HostPort":"443"}],"6443/tcp":[{"HostIp":"0.0.0.0","HostPort":"64713"}],
			"30500/tcp":[{"HostIp":"127.0.0.1","HostPort":"5510"}]}},
	"Mounts":[
		{"Type":"volume","Name":"k3d-skali-dev-images","Destination":"/k3d/images","RW":true},
		{"Type":"volume","Name":"40d3455b","Destination":"/var/lib/rancher/k3s","RW":true},
		{"Type":"volume","Name":"0de5e553","Destination":"/var/lib/kubelet","RW":true}],
	"NetworkSettings":{"Networks":{"k3d-skali-dev":{"IPAddress":"172.20.0.3","IPAMConfig":{"IPv4Address":"172.20.0.3"},
		"Aliases":["k3d-skali-dev-server-0","a5dc668700c8"]}}}}]`

func decodeFixtures(t *testing.T) (*containerInspect, *imageInspect) {
	t.Helper()
	var containers []containerInspect
	require.NoError(t, json.Unmarshal([]byte(nodeContainerFixture), &containers))
	var images []imageInspect
	require.NoError(t, json.Unmarshal([]byte(nodeImageFixture), &images))
	return &containers[0], &images[0]
}

// The spec keeps exactly what k3d layered on the image, so the new k3s
// image brings its own PATH and labels while the node keeps its identity.
func TestNodeSpecFromInspect(t *testing.T) {
	container, image := decodeFixtures(t)
	spec, err := specFromInspect(container, image)
	require.NoError(t, err)
	require.Equal(t, "rancher/k3s:v1.36.3-k3s1", spec.Image)
	require.Equal(t, []string{"K3S_KUBECONFIG_OUTPUT=/output/kubeconfig.yaml", "K3S_TOKEN=secret"}, spec.Env)
	require.Equal(t, map[string]string{"app": "k3d", "k3d.cluster": "skali-dev", "k3d.role": "server", "k3d.server.api.port": "64713"}, spec.Labels)
	require.Equal(t, []string{"/bin/k3d-entrypoint.sh"}, spec.Entrypoint)
	require.Equal(t, []string{"server", "--tls-san", "0.0.0.0"}, spec.Cmd)
	require.Equal(t, "172.20.0.3", spec.IP)
	require.Equal(t, []string{"k3d-skali-dev-server-0"}, spec.Aliases, "docker adds the short ID itself")
	require.Equal(t, []string{"0.0.0.0:64713:6443/tcp", "127.0.0.1:443:443/tcp", "127.0.0.1:5510:30500/tcp"}, spec.Ports)
	require.Equal(t, "/k3d/images", spec.Mounts[0].Destination)
	require.Equal(t, "40d3455b", spec.Mounts[2].Source)

	args := strings.Join(spec.createArgs("skali-dev-upgrade", "rancher/k3s:v1.37.0-k3s1"), " ")
	for _, fragment := range []string{
		"container create --name skali-dev-upgrade --hostname k3d-skali-dev-server-0 --entrypoint /bin/k3d-entrypoint.sh",
		"--privileged --init --restart unless-stopped --tmpfs /run --tmpfs /var/run --security-opt label=disable --cgroupns private",
		"--network k3d-skali-dev --ip 172.20.0.3 --network-alias k3d-skali-dev-server-0",
		"-p 127.0.0.1:443:443/tcp",
		"-v 40d3455b:/var/lib/rancher/k3s",
		"-e K3S_TOKEN=secret",
		"--label k3d.cluster=skali-dev",
		"rancher/k3s:v1.37.0-k3s1 server --tls-san 0.0.0.0",
	} {
		require.Contains(t, args, fragment)
	}
	require.NotContains(t, args, "PATH=", "the new image brings its own PATH")
	require.NotContains(t, args, "org.opencontainers", "the new image brings its own labels")

	// A multi-word entrypoint keeps its argv: the rest leads the command.
	spec.Entrypoint = []string{"/bin/sh", "-c"}
	require.Contains(t, strings.Join(spec.createArgs("n", "i"), " "), "--entrypoint /bin/sh")
	require.Contains(t, strings.Join(spec.createArgs("n", "i"), " "), "i -c server --tls-san 0.0.0.0")
}

// The staged container must reproduce the node; anything else stops the
// replacement while the old node still stands.
func TestSameNode(t *testing.T) {
	container, image := decodeFixtures(t)
	spec, err := specFromInspect(container, image)
	require.NoError(t, err)
	staged := *spec
	staged.Image = "rancher/k3s:v1.37.0-k3s1"
	staged.Mounts = append(append([]nodeMount{}, spec.Mounts...),
		nodeMount{Type: "volume", Source: "fresh", Destination: "/var/lib/new"})
	require.NoError(t, sameNode(spec, &staged), "a new image may declare another volume")

	staged.Mounts = spec.Mounts[1:]
	require.ErrorContains(t, sameNode(spec, &staged), "mount k3d-skali-dev-images:/k3d/images")

	staged.Mounts = spec.Mounts
	staged.IP = "172.20.0.4"
	require.ErrorContains(t, sameNode(spec, &staged), "172.20.0.4")
}

func TestParseCarriedPaths(t *testing.T) {
	diff := "A /output\nA /output/kubeconfig.yaml\nC /bin\nA /bin/k3d-entrypoint-cgroupv2.sh\n" +
		"A /bin/k3d-entrypoint.sh\nC /bin/aux/iptables\nC /etc\nA /etc/rancher\nA /etc/rancher/k3s\n" +
		"A /etc/rancher/node/password\nA /etc/resolv.conf.original\n"
	require.Equal(t, []string{"/bin/k3d-entrypoint-cgroupv2.sh", "/bin/k3d-entrypoint.sh", "/etc/rancher"},
		parseCarriedPaths(diff))
}
