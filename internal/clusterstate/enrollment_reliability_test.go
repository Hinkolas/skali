package clusterstate

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/util/flowcontrol"

	"github.com/Hinkolas/skali/internal/layout"
)

func enrollmentFixture(t *testing.T) (*Store, string, HostFacts, []byte) {
	t.Helper()
	state, err := NewSeedState("kilohertz", Node{ID: uuid.NewString(), InstallationID: "seed-install", Name: "ctrl-01", Role: layout.RoleServer, Capabilities: slicesAllCaps()}, time.Now())
	require.NoError(t, err)
	store := &Store{Client: fake.NewSimpleClientset()}
	_, err = store.Bootstrap(context.Background(), state)
	require.NoError(t, err)
	_, token, err := store.CreateInvitation(context.Background(), layout.RoleAgent, []string{layout.CapabilityDatabase}, time.Hour)
	require.NoError(t, err)
	host := HostFacts{NodeID: uuid.NewString(), InstallationID: uuid.NewString(), Name: "db-01", NodeIP: "10.10.1.10", Capabilities: []string{layout.CapabilityDatabase}, AgentVersion: "test"}
	_, csr, err := NewAgentKeyAndCSR(host.NodeID, host.Name)
	require.NoError(t, err)
	return store, token, host, csr
}
func slicesAllCaps() []string { return append([]string(nil), layout.Capabilities...) }

func startEnrollmentServer(t *testing.T, store *Store, wrap func(http.Handler) http.Handler) *httptest.Server {
	t.Helper()
	coordinator := &Coordinator{Store: store}
	handler := coordinator.Handler()
	if wrap != nil {
		handler = wrap(handler)
	}
	server := httptest.NewUnstartedServer(handler)
	var err error
	server.TLS, err = coordinator.TLSConfig(context.Background())
	require.NoError(t, err)
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func TestEnrollmentLostResponseRetriesSameIdentity(t *testing.T) {
	store, token, host, csr := enrollmentFixture(t)
	var requests atomic.Int32
	server := startEnrollmentServer(t, store, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/enroll" && requests.Add(1) == 1 {
				response := httptest.NewRecorder()
				next.ServeHTTP(response, r)
				if response.Code != http.StatusCreated {
					t.Errorf("first enrollment: %s", response.Body.String())
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				conn.Close() // Coordinator committed but the caller never sees its credentials.
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	client := EnrollmentClient{Endpoint: server.URL, Token: token, RetryBudget: 5 * time.Second}
	response, err := client.Enroll(context.Background(), host, csr)
	require.NoError(t, err)
	require.Equal(t, host.NodeID, response.NodeID)
	require.EqualValues(t, 2, requests.Load())
	state, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, state.Nodes, 2)
	require.Len(t, state.Revisions, 2)
}

func TestPreflightDefaultsAndCapabilityAuthority(t *testing.T) {
	store, token, host, csr := enrollmentFixture(t)
	server := startEnrollmentServer(t, store, nil)
	client := EnrollmentClient{Endpoint: server.URL, Token: token}
	host.Capabilities = nil
	response, err := client.Preflight(context.Background(), host)
	require.NoError(t, err)
	require.Equal(t, []string{layout.CapabilityDatabase}, response.AllowedCapabilities)
	host.Capabilities = []string{layout.CapabilityEdge}
	_, err = client.Enroll(context.Background(), host, csr)
	var problem *EnrollmentError
	require.ErrorAs(t, err, &problem)
	require.Equal(t, http.StatusUnauthorized, problem.Status)
	require.Equal(t, "invitation_rejected", problem.Code)
}

func TestCancelledEnrollmentNeverResurrectsAndReleasesName(t *testing.T) {
	store, token, host, csr := enrollmentFixture(t)
	ctx := context.Background()
	server := startEnrollmentServer(t, store, nil)
	client := EnrollmentClient{Endpoint: server.URL, Token: token}
	_, err := client.Enroll(ctx, host, csr)
	require.NoError(t, err)
	state, err := store.Update(ctx, func(state *State) error {
		cancelled, err := StageNodeRemoval(state, host.Name, time.Now())
		require.True(t, cancelled)
		return err
	})
	require.NoError(t, err)
	from, err := state.Converged()
	require.NoError(t, err)
	to, err := state.Candidate()
	require.NoError(t, err)
	plan, err := BuildPlan(state, from, to, false)
	require.NoError(t, err)
	require.Empty(t, plan.Actions)
	_, err = client.Enroll(ctx, host, csr)
	require.ErrorContains(t, err, "cancelled")
	_, err = client.Preflight(ctx, host)
	require.ErrorContains(t, err, "cancelled")
	// An old, authenticated agent receives cleanup even if it reports active.
	requestBody, err := json.Marshal(AgentPollRequest{Report: AgentReport{Phase: NodePhaseActive}})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/v1/agent/poll", bytes.NewReader(requestBody))
	request.TLS = nodeTLS(host.NodeID)
	recorder := httptest.NewRecorder()
	(&Coordinator{Store: store}).Handler().ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	var poll AgentPollResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &poll))
	require.Equal(t, "cleanup", poll.Action.Type)
	state, err = store.Load(ctx)
	require.NoError(t, err)
	require.Equal(t, NodePhaseCancelled, state.Nodes[host.NodeID].Phase)
	_, token2, err := store.CreateInvitation(ctx, layout.RoleAgent, host.Capabilities, time.Hour)
	require.NoError(t, err)
	client.Token = token2
	host.NodeID, host.InstallationID = uuid.NewString(), uuid.NewString()
	_, csr, err = NewAgentKeyAndCSR(host.NodeID, host.Name)
	require.NoError(t, err)
	_, err = client.Enroll(ctx, host, csr)
	require.NoError(t, err)
}

func nodeTLS(id string) *tls.ConnectionState {
	return &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{Subject: pkix.Name{CommonName: id, Organization: []string{"skali-nodes"}}}}}}
}

func TestRetryClassificationAndBudget(t *testing.T) {
	for _, status := range []int{408, 429, 500, 502, 503, 504} {
		require.True(t, retryableEnrollmentError(&EnrollmentError{Status: status}))
	}
	for _, status := range []int{400, 401, 403, 404, 409} {
		require.False(t, retryableEnrollmentError(&EnrollmentError{Status: status}))
	}
	require.False(t, retryableEnrollmentError(&coordinatorTrustError{errors.New("wrong CA")}))
	require.False(t, retryableEnrollmentError(&net.DNSError{IsNotFound: true}))
	require.True(t, retryableEnrollmentError(io.ErrUnexpectedEOF))
	require.Equal(t, 3*time.Second, retryAfter("3", time.Now()))
	store, token, host, _ := enrollmentFixture(t)
	server := startEnrollmentServer(t, store, func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "60"); w.WriteHeader(503) })
	})
	started := time.Now()
	_, err := (EnrollmentClient{Endpoint: server.URL, Token: token, RetryBudget: 50 * time.Millisecond}).Preflight(context.Background(), host)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (EnrollmentClient{Endpoint: server.URL, Token: token}).Preflight(ctx, host)
	require.ErrorIs(t, err, context.Canceled)
}

func TestMirrorFailureCannotFailCommittedMutation(t *testing.T) {
	store, _, _, _ := enrollmentFixture(t)
	ctx := context.Background()
	fakeClient := store.Client.(*fake.Clientset)
	fakeClient.PrependReactor("create", "configmaps", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("mirror unavailable")
	})
	state, err := store.Update(ctx, func(s *State) error { s.Sequence++; return nil })
	require.NoError(t, err)
	require.NotNil(t, state)
	var failingMirrors MirrorReconciler
	require.ErrorContains(t, failingMirrors.Sync(ctx, store), "mirror unavailable")
	// A lost mirror is repaired after restart; unchanged history causes no writes.
	fakeClient.ReactionChain = fakeClient.ReactionChain[1:]
	var mirrors MirrorReconciler
	require.NoError(t, mirrors.Sync(ctx, store))
	fakeClient.ClearActions()
	require.NoError(t, mirrors.Sync(ctx, store))
	require.Len(t, fakeClient.Actions(), 1, "steady-state mirroring only reads the authority")
	var name string
	for id := range state.Revisions {
		name = "skali-revision-" + id
		break
	}
	require.NoError(t, fakeClient.CoreV1().ConfigMaps(Namespace).Delete(ctx, name, metav1.DeleteOptions{}))
	mirrors.refreshed = time.Time{}
	require.NoError(t, mirrors.Sync(ctx, store))
	_, err = fakeClient.CoreV1().ConfigMaps(Namespace).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
}

func TestElevenHeartbeatsDoNotMirrorRevisionHistory(t *testing.T) {
	store, _, _, _ := enrollmentFixture(t)
	ctx := context.Background()
	state, err := store.Update(ctx, func(s *State) error {
		for i := 0; i < 10; i++ {
			id := uuid.NewString()
			s.Nodes[id] = Node{ID: id, InstallationID: uuid.NewString(), Name: fmt.Sprintf("node-%d", i), Phase: NodePhaseActive, Role: layout.RoleAgent, Capabilities: []string{layout.CapabilityApplication}}
		}
		for i := 0; i < 150; i++ {
			if _, err := s.EditCandidate(time.Now(), func(map[string]RevisionNode, *PlatformState) error { return nil }); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)
	fakeClient := store.Client.(*fake.Clientset)
	fakeClient.ClearActions()
	// Each poll needs four API requests with ActionFor: eleven hosts at five
	// seconds already require 8.8 QPS before leases, enrollment, or mirrors.
	limiter := flowcontrol.NewTokenBucketRateLimiter(50, 100)
	fakeClient.PrependReactor("*", "*", func(ktesting.Action) (bool, runtime.Object, error) { return false, nil, limiter.Wait(ctx) })
	coordinator := &Coordinator{Store: store, ActionFor: func(ctx context.Context, id string) (AgentAction, error) {
		_, err := store.Load(ctx)
		return AgentAction{}, err
	}}
	var wg sync.WaitGroup
	started := time.Now()
	for id := range state.Nodes {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			for j := 0; j < 3; j++ {
				request := httptest.NewRequest(http.MethodPost, "/v1/agent/poll", strings.NewReader(`{"report":{}}`))
				request.TLS = nodeTLS(id)
				response := httptest.NewRecorder()
				coordinator.Handler().ServeHTTP(response, request)
				if response.Code != 200 {
					t.Errorf("heartbeat: %s", response.Body.String())
				}
			}
		}(id)
	}
	wg.Wait()
	require.Less(t, time.Since(started), 5*time.Second)
	actions := fakeClient.Actions()
	require.Len(t, actions, 11*3*4)
	for _, action := range actions {
		if named, ok := action.(interface{ GetName() string }); ok {
			require.Equal(t, StateName, named.GetName())
		}
	}
	t.Logf("33 heartbeats, 151 historical revisions: %d API requests in %s; steady heartbeat demand 8.8 QPS", len(actions), time.Since(started))
}

func TestRemovalOfTargetedNodeRetainsDrainLifecycle(t *testing.T) {
	store, token, host, csr := enrollmentFixture(t)
	server := startEnrollmentServer(t, store, nil)
	_, err := (EnrollmentClient{Endpoint: server.URL, Token: token}).Enroll(context.Background(), host, csr)
	require.NoError(t, err)
	state, err := store.Update(context.Background(), func(state *State) error {
		_, _, err := FreezeCandidate(state, false, time.Now())
		return err
	})
	require.NoError(t, err)
	cancelled, err := StageNodeRemoval(state, host.Name, time.Now())
	require.NoError(t, err)
	require.False(t, cancelled)
	require.Equal(t, NodePhaseAwaitingApply, state.Nodes[host.NodeID].Phase)
	from, err := state.Target()
	require.NoError(t, err)
	to, err := state.Candidate()
	require.NoError(t, err)
	plan, err := BuildPlan(state, from, to, false)
	require.NoError(t, err)
	require.Contains(t, plan.Actions, Action{Kind: ActionRemoveAgent, NodeID: host.NodeID, NodeName: host.Name, Role: layout.RoleAgent, From: host.Capabilities, Destructive: true})
}

func TestEnrollmentCannotOverwriteAnotherNodeIdentity(t *testing.T) {
	store, token, host, csr := enrollmentFixture(t)
	server := startEnrollmentServer(t, store, nil)
	client := EnrollmentClient{Endpoint: server.URL, Token: token}
	_, err := client.Enroll(context.Background(), host, csr)
	require.NoError(t, err)
	host.Name = "another-name"
	host.InstallationID = uuid.NewString()
	_, replacement, err := store.CreateInvitation(context.Background(), layout.RoleAgent, host.Capabilities, time.Hour)
	require.NoError(t, err)
	client.Token = replacement
	_, err = client.Enroll(context.Background(), host, csr)
	require.ErrorContains(t, err, "different installation identity")
}

func TestEnrollmentFallsBackToAnotherPinnedCoordinator(t *testing.T) {
	store, token, host, _ := enrollmentFixture(t)
	healthy := startEnrollmentServer(t, store, nil)
	down := startEnrollmentServer(t, store, func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	})
	result, err := (EnrollmentClient{Endpoint: down.URL, Endpoints: []string{healthy.URL}, Token: token, RetryBudget: 5 * time.Second}).Preflight(context.Background(), host)
	require.NoError(t, err)
	require.Equal(t, "kilohertz", result.Cluster)
}

// fake clients do not enforce Kubernetes resourceVersion conflicts themselves.
func enforceStateCAS(client *fake.Clientset) {
	var version int
	client.PrependReactor("update", "configmaps", func(action ktesting.Action) (bool, runtime.Object, error) {
		incoming := action.(ktesting.UpdateAction).GetObject().(*corev1.ConfigMap)
		if incoming.Name != StateName {
			return false, nil, nil
		}
		current, err := client.Tracker().Get(action.GetResource(), incoming.Namespace, incoming.Name)
		if err != nil {
			return true, nil, err
		}
		if current.(*corev1.ConfigMap).ResourceVersion != incoming.ResourceVersion {
			return true, nil, apierrors.NewConflict(stateResource, StateName, errors.New("stale version"))
		}
		version++
		updated := incoming.DeepCopy()
		updated.ResourceVersion = fmt.Sprint(version)
		err = client.Tracker().Update(action.GetResource(), updated, updated.Namespace)
		return true, updated, err
	})
}

func TestCancellationAndApplySerializeOnAuthority(t *testing.T) {
	for _, applyFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(applyFirst), func(t *testing.T) {
			store, token, host, csr := enrollmentFixture(t)
			server := startEnrollmentServer(t, store, nil)
			_, err := (EnrollmentClient{Endpoint: server.URL, Token: token}).Enroll(context.Background(), host, csr)
			require.NoError(t, err)
			enforceStateCAS(store.Client.(*fake.Clientset))
			loaded := make(chan struct{})
			release := make(chan struct{})
			done := make(chan error, 1)
			var first atomic.Bool
			var cancelled bool
			go func() {
				_, err := store.Update(context.Background(), func(state *State) error {
					if first.CompareAndSwap(false, true) {
						close(loaded)
						<-release
					}
					var err error
					if applyFirst {
						cancelled, err = StageNodeRemoval(state, host.Name, time.Now())
					} else {
						_, _, err = FreezeCandidate(state, false, time.Now())
					}
					return err
				})
				done <- err
			}()
			<-loaded
			_, err = store.Update(context.Background(), func(state *State) error {
				if applyFirst {
					_, _, err := FreezeCandidate(state, false, time.Now())
					return err
				}
				_, err := StageNodeRemoval(state, host.Name, time.Now())
				return err
			})
			close(release)
			require.NoError(t, err)
			require.NoError(t, <-done)
			state, err := store.Load(context.Background())
			require.NoError(t, err)
			if applyFirst {
				require.False(t, cancelled)
				require.Equal(t, NodePhaseAwaitingApply, state.Nodes[host.NodeID].Phase)
			} else {
				require.Equal(t, NodePhaseCancelled, state.Nodes[host.NodeID].Phase)
				require.Empty(t, state.CurrentOperation)
			}
		})
	}
}

func TestBoundInvitationSurvivesStateWriteTimeout(t *testing.T) {
	store, token, host, csr := enrollmentFixture(t)
	var failed atomic.Bool
	store.Client.(*fake.Clientset).PrependReactor("update", "configmaps", func(action ktesting.Action) (bool, runtime.Object, error) {
		if failed.CompareAndSwap(false, true) {
			return true, nil, apierrors.NewTimeoutError("injected state write timeout", 1)
		}
		return false, nil, nil
	})
	server := startEnrollmentServer(t, store, nil)
	_, err := (EnrollmentClient{Endpoint: server.URL, Token: token, RetryBudget: 5 * time.Second}).Enroll(context.Background(), host, csr)
	require.NoError(t, err)
	state, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, state.Nodes, 2)
	require.Len(t, state.Revisions, 2)
}

func TestEnrollmentWhileElevenNodesHeartbeatUnderThrottling(t *testing.T) {
	store, token, joining, csr := enrollmentFixture(t)
	state, err := store.Update(context.Background(), func(s *State) error {
		for i := 0; i < 10; i++ {
			id := uuid.NewString()
			s.Nodes[id] = Node{ID: id, Name: fmt.Sprintf("existing-%d", i), Role: layout.RoleAgent, Phase: NodePhaseAwaitingApply}
		}
		for i := 0; i < 100; i++ {
			_, err := s.EditCandidate(time.Now(), func(map[string]RevisionNode, *PlatformState) error { return nil })
			if err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)
	client := store.Client.(*fake.Clientset)
	enforceStateCAS(client)
	limiter := flowcontrol.NewTokenBucketRateLimiter(50, 10)
	client.PrependReactor("*", "*", func(ktesting.Action) (bool, runtime.Object, error) {
		return false, nil, limiter.Wait(context.Background())
	})
	server := startEnrollmentServer(t, store, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for id := range state.Nodes {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			for i := 0; i < 3; i++ {
				request := httptest.NewRequest(http.MethodPost, "/v1/agent/poll", strings.NewReader(`{"report":{}}`)).WithContext(ctx)
				request.TLS = nodeTLS(id)
				response := httptest.NewRecorder()
				(&Coordinator{Store: store}).Handler().ServeHTTP(response, request)
				// Contended heartbeats may retry next poll; they must not return auth failure.
				if response.Code != 200 && response.Code != 503 {
					t.Errorf("poll returned %d: %s", response.Code, response.Body.String())
				}
			}
		}(id)
	}
	started := time.Now()
	_, err = (EnrollmentClient{Endpoint: server.URL, Token: token, RetryBudget: 10 * time.Second}).Enroll(ctx, joining, csr)
	require.NoError(t, err)
	wg.Wait()
	state, err = store.Load(ctx)
	require.NoError(t, err)
	require.Contains(t, state.Nodes, joining.NodeID)
	require.Len(t, state.Revisions, 102)
	t.Logf("enrollment and 33 concurrent heartbeats with CAS conflicts and 50 QPS throttling: %s", time.Since(started))
}

func TestCancelledAgentAcknowledgementDoesNotRescheduleCleanup(t *testing.T) {
	store, _, host, _ := enrollmentFixture(t)
	_, err := store.Update(context.Background(), func(state *State) error {
		state.Nodes[host.NodeID] = Node{ID: host.NodeID, Name: host.Name, Phase: NodePhaseCancelled}
		return nil
	})
	require.NoError(t, err)
	body, err := json.Marshal(AgentPollRequest{Report: AgentReport{ActionID: "cleanup-" + host.NodeID, ActionOK: true, Phase: NodePhaseRemoved}})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/v1/agent/poll", bytes.NewReader(body))
	request.TLS = nodeTLS(host.NodeID)
	response := httptest.NewRecorder()
	(&Coordinator{Store: store}).Handler().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	var poll AgentPollResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &poll))
	require.Empty(t, poll.Action.Type)
	state, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, NodePhaseCancelled, state.Nodes[host.NodeID].Phase)
	require.Equal(t, "cleanup-complete", state.Nodes[host.NodeID].LastAction)
}

func TestRemoveFinishesLegacyUnappliedCancellation(t *testing.T) {
	store, _, host, _ := enrollmentFixture(t)
	state, err := store.Load(context.Background())
	require.NoError(t, err)
	state.Nodes[host.NodeID] = Node{ID: host.NodeID, Name: host.Name, Phase: NodePhaseUninstalling, LastAction: "cancel-enrollment"}
	cancelled, err := StageNodeRemoval(state, host.Name, time.Now())
	require.NoError(t, err)
	require.True(t, cancelled)
	require.Equal(t, NodePhaseCancelled, state.Nodes[host.NodeID].Phase)
	revision := state.CandidateRevision
	cancelled, err = StageNodeRemoval(state, host.Name, time.Now())
	require.NoError(t, err)
	require.True(t, cancelled)
	require.Equal(t, revision, state.CandidateRevision)
}
