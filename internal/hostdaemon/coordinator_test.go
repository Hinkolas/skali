package hostdaemon

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/installer/host"
)

// addressRunner scripts the two host probes the listener resolution makes.
func addressRunner(t *testing.T, record *installer.Record, ipOutput string) *host.Fake {
	t.Helper()
	runner := &host.Fake{
		FS: map[string][]byte{},
		Handlers: map[string]func(host.Command) (host.Result, error){
			"ip": func(cmd host.Command) (host.Result, error) {
				if len(cmd.Args) > 0 && cmd.Args[0] == "route" {
					return host.Result{Stdout: "1.1.1.1 via 203.0.113.1 dev eth0 src 203.0.113.7 uid 0\n"}, nil
				}
				return host.Result{Stdout: ipOutput}, nil
			},
		},
	}
	if record != nil {
		data, err := yaml.Marshal(record)
		require.NoError(t, err)
		runner.FS[installer.RecordPath] = data
	}
	return runner
}

const twoInterfaces = `1: lo    inet 127.0.0.1/8 scope host lo\       valid_lft forever
2: eth0    inet 203.0.113.7/32 metric 100 scope global dynamic eth0\       valid_lft forever
3: enp7s0    inet 10.0.1.2/32 metric 200 scope global dynamic enp7s0\       valid_lft forever
`

func TestResolveListenAddresses(t *testing.T) {
	t.Parallel()
	node := corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "cp-1"},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{Type: corev1.NodeHostName, Address: "cp-1"},
			{Type: corev1.NodeInternalIP, Address: "203.0.113.7"},
		}},
	}
	newRecord := func(network installer.NodeNetwork) *installer.Record {
		record := &installer.Record{
			Version: installer.RecordVersionReconciled, InstallationID: "install-1",
			Provider: installer.ProviderK3s, Cluster: "production",
			Node: installer.NodeRecord{Name: "cp-1", Role: "server"},
		}
		record.Node.SetNetwork(network)
		return record
	}

	cases := map[string]struct {
		record *installer.Record
		want   []string
	}{
		// The live k3s address is always bound, so agents enrolled before
		// the declaration existed keep their endpoint.
		"no record": {record: nil, want: []string{"203.0.113.7:6444"}},
		"private cluster address adds a listener": {
			record: newRecord(installer.NodeNetwork{ClusterIP: "10.0.1.2"}),
			want:   []string{"10.0.1.2:6444", "203.0.113.7:6444"},
		},
		"public scope is not bound unless asked": {
			record: newRecord(installer.NodeNetwork{
				ClusterIP: "10.0.1.2", PublicIPs: []string{"198.51.100.9"},
			}),
			want: []string{"10.0.1.2:6444", "203.0.113.7:6444"},
		},
		// A declared address that is not assigned here (a floating or
		// NAT-mapped one) is reported, never bound: binding it would fail
		// and take the whole coordinator down.
		"unassigned public address is skipped": {
			record: newRecord(installer.NodeNetwork{
				ClusterIP: "10.0.1.2", PublicIPs: []string{"198.51.100.9"},
				CoordinatorBind: []string{installer.NetworkScopeCluster, installer.NetworkScopePublic},
			}),
			want: []string{"10.0.1.2:6444", "203.0.113.7:6444"},
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			daemon := &CoordinatorDaemon{
				Runner: addressRunner(t, testCase.record, twoInterfaces),
			}
			got, err := daemon.resolveListenAddresses(context.Background(), node)
			require.NoError(t, err)
			require.Equal(t, testCase.want, got)
		})
	}
}

// A node whose declared public address is really assigned binds it when
// the operator opted into the public scope, so a node outside the private
// network can still enroll.
func TestResolveListenAddressesBindsAssignedPublic(t *testing.T) {
	t.Parallel()
	record := &installer.Record{
		Version: installer.RecordVersionReconciled, InstallationID: "install-1",
		Provider: installer.ProviderK3s, Cluster: "production",
		Node: installer.NodeRecord{Name: "cp-1", Role: "server"},
	}
	record.Node.SetNetwork(installer.NodeNetwork{
		ClusterIP: "10.0.1.2", PublicIPs: []string{"203.0.113.7"},
		CoordinatorBind: []string{installer.NetworkScopeCluster, installer.NetworkScopePublic},
	})
	daemon := &CoordinatorDaemon{Runner: addressRunner(t, record, twoInterfaces)}
	got, err := daemon.resolveListenAddresses(context.Background(), corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "cp-1"},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{Type: corev1.NodeInternalIP, Address: "10.0.1.2"},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"10.0.1.2:6444", "203.0.113.7:6444"}, got)
}

func TestResolveListenAddressesRefusesWithoutAddress(t *testing.T) {
	t.Parallel()
	daemon := &CoordinatorDaemon{Runner: addressRunner(t, nil, "")}
	_, err := daemon.resolveListenAddresses(context.Background(), corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "cp-1"},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{Type: corev1.NodeHostName, Address: "cp-1"},
		}},
	})
	require.ErrorContains(t, err, "no valid InternalIP")
}
