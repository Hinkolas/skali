package clusterstate

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

const maxRequestBody = 1 << 20

type Coordinator struct {
	Store      *Store
	Logger     *slog.Logger
	ActionFor  func(context.Context, string) (AgentAction, error)
	ActionDone func(context.Context, string, AgentReport) error
}

func (c *Coordinator) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", c.health)
	mux.HandleFunc("POST /v1/enroll/preflight", c.preflight)
	mux.HandleFunc("POST /v1/enroll", c.enroll)
	mux.HandleFunc("POST /v1/agent/poll", c.poll)
	mux.HandleFunc("POST /v1/agent/renew", c.renew)
	return c.recover(mux)
}

func (c *Coordinator) TLSConfig(ctx context.Context) (*tls.Config, error) {
	trust, err := c.Store.Trust(ctx)
	if err != nil {
		return nil, err
	}
	certificate, err := tls.X509KeyPair(trust.ServerCert, trust.ServerKey)
	if err != nil {
		return nil, fmt.Errorf("load coordinator TLS keypair: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(trust.CACert) {
		return nil, errors.New("coordinator CA contains no certificate")
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    pool,
	}, nil
}

func (c *Coordinator) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (c *Coordinator) preflight(w http.ResponseWriter, r *http.Request) {
	token, ok := tokenFromRequest(r)
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "missing enrollment token")
		return
	}
	var request PreflightRequest
	if !decodeRequest(w, r, &request) {
		return
	}
	invitation, err := c.Store.CheckInvitationFor(r.Context(), token,
		request.Host.InstallationID, request.Host.Capabilities)
	if err != nil {
		writeProblem(w, http.StatusUnauthorized, err.Error())
		return
	}
	state, err := c.Store.Load(r.Context())
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if state.Decommissioning {
		writeProblem(w, http.StatusServiceUnavailable, "cluster is decommissioning")
		return
	}
	if err := validateEnrollmentHost(state, request.Host); err != nil {
		writeProblem(w, http.StatusConflict, err.Error())
		return
	}
	if err := validateServerAddress(invitation.Role, request.Host); err != nil {
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, PreflightResponse{
		Cluster: state.Cluster, Role: invitation.Role,
		Coordinators: coordinatorEndpoints(state),
	})
}

func (c *Coordinator) enroll(w http.ResponseWriter, r *http.Request) {
	token, ok := tokenFromRequest(r)
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "missing enrollment token")
		return
	}
	var request EnrollRequest
	if !decodeRequest(w, r, &request) {
		return
	}
	if request.Host.NodeID == "" {
		writeProblem(w, http.StatusBadRequest, "node ID is required")
		return
	}
	invitation, err := c.Store.BindInvitation(r.Context(), token,
		request.Host.InstallationID, request.Host.Capabilities, []byte(request.CSR))
	if err != nil {
		writeProblem(w, http.StatusUnauthorized, err.Error())
		return
	}
	now := c.Store.now()
	state, err := c.Store.Update(r.Context(), func(state *State) error {
		if state.Decommissioning {
			return errors.New("cluster is decommissioning")
		}
		if existing, exists := state.Nodes[request.Host.NodeID]; exists &&
			existing.InstallationID == request.Host.InstallationID {
			// A retry after the state write but before the certificate
			// response must not manufacture another candidate revision.
			return nil
		}
		if err := validateEnrollmentHost(state, request.Host); err != nil {
			return err
		}
		if err := validateServerAddress(invitation.Role, request.Host); err != nil {
			return err
		}
		node := Node{
			ID: request.Host.NodeID, InstallationID: request.Host.InstallationID,
			Name: request.Host.Name, Role: invitation.Role,
			Capabilities: append([]string(nil), request.Host.Capabilities...),
			NodeIP:       request.Host.NodeIP, AgentVersion: request.Host.AgentVersion,
			Phase: NodePhaseAwaitingApply, EnrolledAt: now, UpdatedAt: now,
		}
		if invitation.Role == "server" {
			node.Coordinator = "https://" +
				net.JoinHostPort(request.Host.NodeIP, DefaultCoordinatorPort)
		}
		if state.Nodes == nil {
			state.Nodes = make(map[string]Node)
		}
		state.Nodes[node.ID] = node
		_, err := state.EditCandidate(now, func(nodes map[string]RevisionNode, _ *PlatformState) error {
			nodes[node.ID] = revisionNode(node)
			return nil
		})
		return err
	})
	if err != nil {
		writeProblem(w, http.StatusConflict, err.Error())
		return
	}
	certificate, err := c.Store.SignCSR(r.Context(), []byte(request.CSR),
		request.Host.NodeID, request.Host.Name, 7*24*time.Hour)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	}
	trust, err := c.Store.Trust(r.Context())
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, EnrollResponse{
		Cluster: state.Cluster, Role: invitation.Role, NodeID: request.Host.NodeID,
		Certificate: string(certificate), CACert: string(trust.CACert),
		Coordinators: coordinatorEndpoints(state),
	})
}

func (c *Coordinator) renew(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := authenticatedNode(r)
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "a valid node client certificate is required")
		return
	}
	var request RenewRequest
	if !decodeRequest(w, r, &request) {
		return
	}
	state, err := c.Store.Load(r.Context())
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	node, ok := state.Nodes[nodeID]
	if !ok || node.Phase == NodePhaseRemoved {
		writeProblem(w, http.StatusForbidden, "node identity is not active")
		return
	}
	certificate, err := c.Store.SignCSR(r.Context(), []byte(request.CSR),
		nodeID, node.Name, 7*24*time.Hour)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, RenewResponse{Certificate: string(certificate)})
}

func (c *Coordinator) poll(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := authenticatedNode(r)
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "a valid node client certificate is required")
		return
	}
	var request AgentPollRequest
	if !decodeRequest(w, r, &request) {
		return
	}
	state, err := c.Store.Update(r.Context(), func(state *State) error {
		node, exists := state.Nodes[nodeID]
		if !exists || node.Phase == NodePhaseRemoved {
			return errors.New("node identity is not active")
		}
		node.LastSeen = c.Store.now()
		node.K3sVersion = request.Report.K3sVersion
		if request.Report.AgentVersion != "" {
			node.AgentVersion = request.Report.AgentVersion
		}
		if request.Report.Phase != "" {
			node.Phase = request.Report.Phase
		}
		if request.Report.Error != "" {
			node.LastError = request.Report.Error
		} else if request.Report.ActionOK {
			node.LastError = ""
		}
		node.UpdatedAt = c.Store.now()
		state.Nodes[nodeID] = node
		return nil
	})
	if err != nil {
		writeProblem(w, http.StatusForbidden, err.Error())
		return
	}
	if c.ActionDone != nil && request.Report.ActionID != "" {
		if err := c.ActionDone(r.Context(), nodeID, request.Report); err != nil {
			writeProblem(w, http.StatusConflict, err.Error())
			return
		}
	}
	action := AgentAction{}
	if c.ActionFor != nil {
		action, err = c.ActionFor(r.Context(), nodeID)
		if err != nil {
			writeProblem(w, http.StatusServiceUnavailable, err.Error())
			return
		}
	}
	// Every successful poll refreshes the complete active coordinator set.
	// Nodes enrolled before a later server was applied therefore gain that
	// server as a failover endpoint without re-enrollment.
	state, err = c.Store.Load(r.Context())
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	response := AgentPollResponse{
		Action: action, Coordinators: coordinatorEndpoints(state),
		ConvergedRevision: state.ConvergedRevision,
		TargetRevision:    state.TargetRevision, CandidateRevision: state.CandidateRevision,
		OperationID: state.CurrentOperation, RetryAfter: 5 * time.Second,
	}
	if operation, ok := state.Operations[state.CurrentOperation]; ok {
		response.OperationPhase = operation.Phase
	}
	writeJSON(w, http.StatusOK, response)
}

func validateEnrollmentHost(state *State, host HostFacts) error {
	if host.InstallationID == "" || host.Name == "" || host.AgentVersion == "" {
		return errors.New("joining host is missing installation ID, name, or agent version")
	}
	if len(host.Capabilities) == 0 {
		return errors.New("joining host must request at least one capability")
	}
	for id, node := range state.Nodes {
		if node.Phase == NodePhaseRemoved {
			continue
		}
		if host.NodeID != "" && id == host.NodeID &&
			node.InstallationID == host.InstallationID {
			return nil
		}
		if node.InstallationID == host.InstallationID {
			return fmt.Errorf("installation ID is already enrolled as %s", node.Name)
		}
		if node.Name == host.Name {
			return fmt.Errorf("node name %q is already enrolled", host.Name)
		}
	}
	return nil
}

// validateServerAddress refuses a server enrollment that reports no
// address. Every other node builds this server's coordinator endpoint from
// it, and the node name it used to fall back to is a hostname the rest of
// the fleet usually cannot resolve.
func validateServerAddress(role string, host HostFacts) error {
	if role != "server" || strings.TrimSpace(host.NodeIP) != "" {
		return nil
	}
	return errors.New("a joining server must report the address other nodes reach it " +
		"through; pass --node-ip or declare network.clusterIP")
}

func coordinatorEndpoints(state *State) []string {
	var endpoints []string
	for _, node := range state.Nodes {
		if node.Role == "server" && node.Phase == NodePhaseActive && node.Coordinator != "" {
			endpoints = append(endpoints, node.Coordinator)
		}
	}
	slices.Sort(endpoints)
	return slices.Compact(endpoints)
}

func tokenFromRequest(r *http.Request) (Token, bool) {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	value, ok := strings.CutPrefix(value, "Bearer ")
	if !ok {
		return Token{}, false
	}
	token, err := ParseToken(value)
	return token, err == nil
}

func authenticatedNode(r *http.Request) (string, bool) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 ||
		len(r.TLS.VerifiedChains[0]) == 0 {
		return "", false
	}
	certificate := r.TLS.VerifiedChains[0][0]
	if !slices.Contains(certificate.Subject.Organization, "skali-nodes") {
		return "", false
	}
	if _, err := uuid.Parse(certificate.Subject.CommonName); err != nil {
		return "", false
	}
	return certificate.Subject.CommonName, true
}

func decodeRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxRequestBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid request")
		return false
	}
	return true
}

func writeProblem(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (c *Coordinator) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				if c.Logger != nil {
					c.Logger.Error("coordinator request panicked", "panic", recovered)
				}
				writeProblem(w, http.StatusInternalServerError, "internal coordinator error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
