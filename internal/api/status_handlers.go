package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/reconcile"
	"github.com/Hinkolas/skali/internal/revision"
)

// statusHandlers serves topology and health projections. Every read comes
// from the database pointers plus the in-memory observed store; no handler
// here can reach Kubernetes because none holds a cluster client.
type statusHandlers struct {
	reconcile *reconcile.Kernel
}

type revisionRefPayload struct {
	ID       string `json:"id"`
	Checksum string `json:"checksum"`
}

type observationPayload struct {
	State      string     `json:"state"`
	StaleSince *time.Time `json:"stale_since"`
	LastSync   *time.Time `json:"last_sync"`
}

type healthDiagnosticPayload struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Resource string `json:"resource,omitempty"`
}

type podPayload struct {
	Name      string     `json:"name"`
	Node      string     `json:"node"`
	Phase     string     `json:"phase"`
	Ready     bool       `json:"ready"`
	Restarts  int32      `json:"restarts"`
	Reason    string     `json:"reason,omitempty"`
	StartedAt *time.Time `json:"started_at"`
	// Color is the pod's blue-green color (empty on uncolored workloads);
	// Serving reports whether the application's Service selects it.
	Color   string `json:"color,omitempty"`
	Serving bool   `json:"serving"`
}

type serviceStatusPayload struct {
	Key         string                    `json:"key"`
	Type        string                    `json:"type"`
	Health      string                    `json:"health"`
	Diagnostics []healthDiagnosticPayload `json:"diagnostics"`
	Pods        []podPayload              `json:"pods"`
	Routes      []routeStatusPayload      `json:"routes,omitempty"`
	Intercepted bool                      `json:"intercepted,omitempty"`
}

// routeStatusPayload projects one public route with its edge policies and,
// on TLS-capable installations, the observed certificate. certificate is
// null where none exists by design (tls disabled, local installation).
type routeStatusPayload struct {
	Key         string              `json:"key"`
	Domain      string              `json:"domain"`
	Path        string              `json:"path"`
	TLS         string              `json:"tls"`
	Strategy    string              `json:"strategy"`
	Compress    bool                `json:"compress"`
	Certificate *certificatePayload `json:"certificate,omitempty"`
	// Edge is the reconciler's last probe of whether the route's domain
	// reaches this installation; absent where no certificate is expected
	// or no pass probed it yet.
	Edge *edgeStatusPayload `json:"edge,omitempty"`
}

type edgeStatusPayload struct {
	State     string     `json:"state"` // reachable | partial | unreachable | unresolved | unknown
	Message   string     `json:"message,omitempty"`
	CheckedAt *time.Time `json:"checked_at"`
	Addresses []string   `json:"addresses"`
	// Deferred is the reconciler's conclusion: the domain does not reach
	// this installation and no certificate usable for it is on hand.
	Deferred bool `json:"deferred"`
}

type certificatePayload struct {
	FailedAttempts           int32      `json:"failed_attempts"`
	LastFailureTime          *time.Time `json:"last_failure_time"`
	NextRetryTime            *time.Time `json:"next_retry_time"`
	NextPrivateKeySecretName string     `json:"next_private_key_secret_name,omitempty"`
	Name                     string     `json:"name"`
	SecretName               string     `json:"secret_name"`
	State                    string     `json:"state"` // pending | issuing | active | failing | expired
	Reason                   string     `json:"reason,omitempty"`
	Message                  string     `json:"message,omitempty"`
	NotAfter                 *time.Time `json:"not_after"`
	RenewalTime              *time.Time `json:"renewal_time"`
}

type environmentStatusPayload struct {
	EnvironmentID      string                 `json:"environment_id"`
	State              string                 `json:"state"`
	TargetRevision     *revisionRefPayload    `json:"target_revision"`
	ActiveRevision     *revisionRefPayload    `json:"active_revision"`
	Observation        observationPayload     `json:"observation"`
	Platforms          []string               `json:"platforms"`
	PlatformPreference []string               `json:"platform_preference"`
	Services           []serviceStatusPayload `json:"services"`
}

func newObservationPayload(source module.SourceStatus) observationPayload {
	payload := observationPayload{State: source.State}
	if !source.StaleSince.IsZero() {
		staleSince := source.StaleSince
		payload.StaleSince = &staleSince
	}
	if !source.LastSync.IsZero() {
		lastSync := source.LastSync
		payload.LastSync = &lastSync
	}
	return payload
}

func newEnvironmentStatusPayload(status *reconcile.Status) environmentStatusPayload {
	payload := environmentStatusPayload{
		EnvironmentID:      status.EnvironmentID.String(),
		State:              status.State,
		Observation:        newObservationPayload(status.Observation),
		Platforms:          append([]string{}, status.Platforms...),
		PlatformPreference: append([]string{}, status.PlatformPreference...),
		Services:           make([]serviceStatusPayload, 0, len(status.Services)),
	}
	if status.Target != nil {
		payload.TargetRevision = &revisionRefPayload{ID: status.Target.ID.String(), Checksum: status.Target.Checksum}
	}
	if status.Active != nil {
		payload.ActiveRevision = &revisionRefPayload{ID: status.Active.ID.String(), Checksum: status.Active.Checksum}
	}
	for _, service := range status.Services {
		servicePayload := serviceStatusPayload{
			Key:         service.Key,
			Type:        service.Type,
			Health:      string(service.Health),
			Diagnostics: make([]healthDiagnosticPayload, 0, len(service.Diagnostics)),
			Pods:        make([]podPayload, 0, len(service.Pods)),
			Intercepted: service.Intercepted,
		}
		for _, diagnostic := range service.Diagnostics {
			servicePayload.Diagnostics = append(servicePayload.Diagnostics, healthDiagnosticPayload{
				Severity: diagnostic.Severity,
				Code:     diagnostic.Code,
				Message:  diagnostic.Message,
				Resource: diagnostic.Resource,
			})
		}
		for _, pod := range service.Pods {
			podEntry := podPayload{
				Name:     pod.Name,
				Node:     pod.Node,
				Phase:    pod.Phase,
				Ready:    pod.Ready,
				Restarts: pod.Restarts,
				Reason:   pod.Reason,
				Color:    pod.Color,
				Serving:  pod.Serving,
			}
			if !pod.Started.IsZero() {
				started := pod.Started
				podEntry.StartedAt = &started
			}
			servicePayload.Pods = append(servicePayload.Pods, podEntry)
		}
		for _, route := range service.Routes {
			routeEntry := routeStatusPayload{
				Key:      route.Key,
				Domain:   route.Domain,
				Path:     route.Path,
				TLS:      route.TLS,
				Strategy: route.Strategy,
				Compress: route.Compress,
			}
			if route.Certificate != nil {
				certificate := &certificatePayload{
					FailedAttempts:           route.Certificate.FailedAttempts,
					NextPrivateKeySecretName: route.Certificate.NextPrivateKeySecretName,
					Name:                     route.Certificate.Name,
					SecretName:               route.Certificate.SecretName,
					State:                    route.Certificate.State,
					Reason:                   route.Certificate.Reason,
					Message:                  route.Certificate.Message,
				}
				if !route.Certificate.NotAfter.IsZero() {
					notAfter := route.Certificate.NotAfter
					certificate.NotAfter = &notAfter
				}
				if !route.Certificate.RenewalTime.IsZero() {
					renewal := route.Certificate.RenewalTime
					certificate.RenewalTime = &renewal
				}
				if !route.Certificate.LastFailureTime.IsZero() {
					value := route.Certificate.LastFailureTime
					certificate.LastFailureTime = &value
				}
				if !route.Certificate.NextRetryTime.IsZero() {
					value := route.Certificate.NextRetryTime
					certificate.NextRetryTime = &value
				}
				routeEntry.Certificate = certificate
			}
			if route.Edge != nil {
				edge := &edgeStatusPayload{
					State:     route.Edge.State,
					Message:   route.Edge.Message,
					Addresses: append([]string{}, route.Edge.Addresses...),
					Deferred:  route.Edge.Deferred,
				}
				if !route.Edge.CheckedAt.IsZero() {
					checked := route.Edge.CheckedAt
					edge.CheckedAt = &checked
				}
				routeEntry.Edge = edge
			}
			servicePayload.Routes = append(servicePayload.Routes, routeEntry)
		}
		payload.Services = append(payload.Services, servicePayload)
	}
	return payload
}

func (h *statusHandlers) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	status, err := h.reconcile.Status(r.Context(), id)
	if err != nil {
		writeStatusError(r, w, err)
		return
	}
	writeJSON(w, http.StatusOK, newEnvironmentStatusPayload(status))
}

// stream is the projection invalidation stream made client-visible: the
// full status document is re-sent, coalesced, on every invalidation that
// touches the environment. Mounted outside the request timeout.
func (h *statusHandlers) stream(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, codeInternal, "streaming unsupported")
		return
	}
	status, err := h.reconcile.Status(r.Context(), id)
	if err != nil {
		writeStatusError(r, w, err)
		return
	}
	invalidations, cancel := h.reconcile.SubscribeStatus(id)
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(status *reconcile.Status) bool {
		data, err := json.Marshal(newEnvironmentStatusPayload(status))
		if err != nil {
			return false
		}
		fmt.Fprintf(w, "event: status\ndata: %s\n\n", data)
		flusher.Flush()
		return true
	}
	if !send(status) {
		return
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case _, open := <-invalidations:
			if !open {
				// Fell behind; the client reconnects and re-reads.
				return
			}
			// Coalesce whatever queued up; the payload is a full document.
			for {
				select {
				case _, more := <-invalidations:
					if !more {
						return
					}
					continue
				default:
				}
				break
			}
			status, err := h.reconcile.Status(r.Context(), id)
			if err != nil {
				return
			}
			if !send(status) {
				return
			}
		}
	}
}

type nodePayload struct {
	Name           string     `json:"name"`
	Role           string     `json:"role"`
	Capabilities   []string   `json:"capabilities"`
	Arch           string     `json:"arch,omitempty"`
	OS             string     `json:"os,omitempty"`
	KubeletVersion string     `json:"kubelet_version,omitempty"`
	Ready          bool       `json:"ready"`
	Schedulable    bool       `json:"schedulable"`
	InternalIP     string     `json:"internal_ip,omitempty"`
	ExternalIP     string     `json:"external_ip,omitempty"`
	LastHeartbeat  *time.Time `json:"last_heartbeat"`
	// MemoryAllocatableBytes is omitted until the kubelet reported it.
	MemoryAllocatableBytes int64 `json:"memory_allocatable_bytes,omitempty"`
}

// nodes serves the member-visible node projection. The observation payload
// rides along so clients can tell "no nodes" from "not synced yet".
func (h *statusHandlers) nodes(w http.ResponseWriter, r *http.Request) {
	records := h.reconcile.Nodes()
	payload := struct {
		Nodes       []nodePayload      `json:"nodes"`
		Observation observationPayload `json:"observation"`
	}{
		Nodes:       make([]nodePayload, 0, len(records)),
		Observation: newObservationPayload(h.reconcile.Observation().Source),
	}
	for _, record := range records {
		entry := nodePayload{
			Name:                   record.Name,
			Role:                   record.Role,
			Capabilities:           append([]string{}, record.Capabilities...),
			Arch:                   record.Arch,
			OS:                     record.OS,
			KubeletVersion:         record.KubeletVersion,
			Ready:                  record.Ready,
			Schedulable:            record.Schedulable,
			InternalIP:             record.InternalIP,
			ExternalIP:             record.ExternalIP,
			MemoryAllocatableBytes: record.MemoryAllocatableBytes,
		}
		if !record.LastHeartbeat.IsZero() {
			heartbeat := record.LastHeartbeat
			entry.LastHeartbeat = &heartbeat
		}
		payload.Nodes = append(payload.Nodes, entry)
	}
	writeJSON(w, http.StatusOK, payload)
}

type kindSyncPayload struct {
	Kind   string `json:"kind"`
	Synced bool   `json:"synced"`
}

type namedSourcePayload struct {
	Name string `json:"name"`
	observationPayload
}

type systemObservationPayload struct {
	Mode        string               `json:"mode"`
	Ready       bool                 `json:"ready"`
	Observation observationPayload   `json:"observation"`
	Sources     []namedSourcePayload `json:"sources"`
	Kinds       []kindSyncPayload    `json:"kinds"`
	QueueDepth  int                  `json:"queue_depth"`
	Workers     int                  `json:"workers"`
}

func (h *statusHandlers) system(w http.ResponseWriter, r *http.Request) {
	info := h.reconcile.Observation()
	payload := systemObservationPayload{
		Mode:        info.Mode,
		Ready:       info.Ready,
		Observation: newObservationPayload(info.Source),
		Sources:     make([]namedSourcePayload, 0, len(info.Sources)),
		Kinds:       make([]kindSyncPayload, 0, len(info.Kinds)),
		QueueDepth:  info.QueueDepth,
		Workers:     info.Workers,
	}
	for _, source := range info.Sources {
		payload.Sources = append(payload.Sources, namedSourcePayload{
			Name:               source.Name,
			observationPayload: newObservationPayload(source.SourceStatus),
		})
	}
	for _, kind := range info.Kinds {
		payload.Kinds = append(payload.Kinds, kindSyncPayload{Kind: kind.Kind, Synced: kind.Synced})
	}
	writeJSON(w, http.StatusOK, payload)
}

func writeStatusError(r *http.Request, w http.ResponseWriter, err error) {
	if errors.Is(err, reconcile.ErrEnvironmentNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, "environment not found")
		return
	}
	var stale *revision.SchemaError
	if errors.As(err, &stale) {
		writeError(w, http.StatusConflict, codeUnsupportedSchema, stale.Error())
		return
	}
	writeInternalError(r.Context(), w, "environment status", err)
}

// routeProbePayload is one manual edge probe of a route domain.
type routeProbePayload struct {
	Service string           `json:"service"`
	Key     string           `json:"key"`
	Domain  string           `json:"domain"`
	Edge    edgeProbePayload `json:"edge"`
}

// edgeProbePayload is the probe's verdict with its per-address detail; the
// reconciler's own conclusion (deferred) follows in the status projection
// once the enqueued pass has acted on it.
type edgeProbePayload struct {
	State     string               `json:"state"` // reachable | partial | unreachable | unresolved | unknown
	Message   string               `json:"message,omitempty"`
	CheckedAt time.Time            `json:"checked_at"`
	Addresses []edgeAddressPayload `json:"addresses"`
}

type edgeAddressPayload struct {
	Address string `json:"address"`
	Outcome string `json:"outcome"` // ours | foreign | unreachable
	Detail  string `json:"detail,omitempty"`
}

// POST /v1/environments/{id}/routes/probe
func (h *statusHandlers) probeRoutes(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	probes, err := h.reconcile.ProbeRoutes(r.Context(), id)
	if errors.Is(err, reconcile.ErrNoEdgeProbe) {
		writeError(w, http.StatusConflict, codeConflict, "this installation has no edge probe: route domains are not checked here")
		return
	}
	if err != nil {
		writeStatusError(r, w, err)
		return
	}
	payload := make([]routeProbePayload, 0, len(probes))
	for _, probe := range probes {
		edge := edgeProbePayload{
			State:     string(probe.Result.State),
			Message:   probe.Result.Message,
			CheckedAt: probe.Result.CheckedAt,
			Addresses: make([]edgeAddressPayload, 0, len(probe.Result.Addresses)),
		}
		for _, address := range probe.Result.Addresses {
			edge.Addresses = append(edge.Addresses, edgeAddressPayload{
				Address: address.Address, Outcome: string(address.Outcome), Detail: address.Detail,
			})
		}
		payload = append(payload, routeProbePayload{Service: probe.Service, Key: probe.Key, Domain: probe.Domain, Edge: edge})
	}
	writeJSON(w, http.StatusOK, struct {
		Routes []routeProbePayload `json:"routes"`
	}{payload})
}
