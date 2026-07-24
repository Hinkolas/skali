package hostdaemon

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCoordinatorListenAddressUsesInternalIP(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		addresses []corev1.NodeAddress
		want      string
		wantErr   string
	}{
		"ipv4": {
			addresses: []corev1.NodeAddress{
				{Type: corev1.NodeHostName, Address: "cp-1"},
				{Type: corev1.NodeInternalIP, Address: "10.1.0.3"},
			},
			want: "10.1.0.3:6444",
		},
		"ipv6": {
			addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "fd00::3"},
			},
			want: "[fd00::3]:6444",
		},
		"missing": {
			addresses: []corev1.NodeAddress{
				{Type: corev1.NodeHostName, Address: "cp-1"},
			},
			wantErr: "has no valid InternalIP",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			node := corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "cp-1"},
				Status:     corev1.NodeStatus{Addresses: testCase.addresses},
			}
			got, err := coordinatorListenAddress(node)
			if testCase.wantErr != "" {
				require.ErrorContains(t, err, testCase.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, testCase.want, got)
		})
	}
}
