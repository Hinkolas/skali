package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/utils"
	"github.com/Hinkolas/skali/internal/valuestore"
)

// appEnvHandlers serves one application's fully resolved environment: the
// exact variable set its pod would receive, with stored values decrypted
// and service outputs materialized. It backs skali dev's host-run
// processes. Like credential reveal it sits behind fresh authentication,
// returns secrets once, and never journals or logs them. The local
// audience rewrites endpoint-bearing outputs to the loopback ports the dev
// cluster maps, and exists only on the local platform.
type appEnvHandlers struct {
	deploy  *deploy.Service
	values  *valuestore.Service
	db      *dbstore.Service
	secrets func(ctx context.Context, namespace, name string) (map[string][]byte, error)
	managed bool
}

type resolvedEnvironmentPayload struct {
	Values   map[string]string `json:"values"`
	Warnings []string          `json:"warnings"`
}

// GET /v1/environments/{id}/applications/{key}/environment
func (h *appEnvHandlers) resolved(w http.ResponseWriter, r *http.Request) {
	environmentID, ok := pathID(w, r)
	if !ok {
		return
	}
	key := chi.URLParam(r, "key")
	audience := r.URL.Query().Get("audience")
	if audience == "" {
		audience = "internal"
	}
	if audience != "internal" && audience != "local" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "audience must be internal or local")
		return
	}
	if audience == "local" && h.managed {
		writeError(w, http.StatusConflict, codeConflict, "the local audience is only available on the local platform")
		return
	}
	portBase := bundle.PoolNodePortMin
	if raw := r.URL.Query().Get("port_base"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 65535 {
			writeError(w, http.StatusBadRequest, codeBadRequest, "port_base must be a port number")
			return
		}
		portBase = parsed
	}
	if h.db == nil || h.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "no cluster available for environment resolution")
		return
	}

	target, err := h.deploy.Target(r.Context(), environmentID)
	if err != nil {
		if errors.Is(err, deploy.ErrEnvironmentNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "not found")
			return
		}
		writeInternalError(r.Context(), w, "get target", err)
		return
	}
	if target.ActiveRevisionID == nil {
		writeError(w, http.StatusConflict, codeConflict, "the environment has no active revision yet; deploy first")
		return
	}
	rev, err := h.deploy.GetRevision(r.Context(), *target.ActiveRevisionID)
	if err != nil {
		writeInternalError(r.Context(), w, "load revision", err)
		return
	}
	application, ok := rev.Definition.Applications[key]
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "the active revision declares no application "+key)
		return
	}

	refs := make(map[string]int, len(rev.Secrets))
	for name, secret := range rev.Secrets {
		refs[name] = secret.Version
	}
	variables, err := h.values.Plaintexts(r.Context(), environmentID, refs)
	if err != nil {
		writeInternalError(r.Context(), w, "resolve values", err)
		return
	}

	outputs, warnings := h.collectOutputs(r.Context(), environmentID, application, audience, portBase)

	payload := resolvedEnvironmentPayload{Values: map[string]string{}, Warnings: warnings}
	for _, name := range utils.SortedKeys(application.Environment) {
		value, err := compiler.ResolveExpressionOutputs(application.Environment[name], variables, outputs)
		if err != nil {
			payload.Warnings = append(payload.Warnings, name+" was omitted: "+err.Error())
			continue
		}
		payload.Values[name] = value
	}
	if payload.Warnings == nil {
		payload.Warnings = []string{}
	}
	writeJSON(w, http.StatusOK, payload)
}

// collectOutputs materializes the outputs the application's environment
// references, rewritten for the audience. An unprovisioned or unreadable
// service degrades to a warning; the variables that need it are omitted by
// the resolver, never guessed.
func (h *appEnvHandlers) collectOutputs(ctx context.Context, environmentID uuid.UUID,
	application compiler.Application, audience string, portBase int) (map[string]map[string]string, []string) {

	referenced := map[string]compiler.ExpressionPart{}
	for _, expression := range application.Environment {
		for _, part := range expression.Parts {
			if part.Kind == "service_output" {
				referenced[part.Collection+"."+part.Service] = part
			}
		}
	}

	hostPort := func(nodePort int) string {
		return strconv.Itoa(portBase + nodePort - bundle.PoolNodePortMin)
	}
	outputs := make(map[string]map[string]string, len(referenced))
	var warnings []string
	for _, dotted := range utils.SortedKeys(referenced) {
		part := referenced[dotted]
		switch part.Collection {
		case "databases":
			row, err := h.db.LiveServiceClaim(ctx, environmentID, part.Service)
			if err != nil || claim.Phase(row.Phase) != claim.PhaseProvisioned {
				warnings = append(warnings, dotted+" is not provisioned yet")
				continue
			}
			tenant, err := h.db.LiveTenant(ctx, row.ID)
			if err != nil {
				warnings = append(warnings, dotted+" has no live tenant")
				continue
			}
			data, err := h.secrets(ctx, "skali-platform", tenant.CredentialSecret)
			if err != nil {
				warnings = append(warnings, dotted+": reading the credential failed")
				continue
			}
			host, port := tenant.Host, strconv.Itoa(int(tenant.Port))
			if audience == "local" {
				cluster, err := h.db.GetCluster(ctx, tenant.ClusterID)
				if err != nil || cluster.NodePort == nil {
					warnings = append(warnings, dotted+" has no loopback port allocated yet")
					continue
				}
				host, port = "127.0.0.1", hostPort(int(*cluster.NodePort))
			}
			username := string(data["username"])
			password := string(data["password"])
			outputs[dotted] = map[string]string{
				"host":     host,
				"port":     port,
				"name":     tenant.DatabaseName,
				"username": username,
				"password": password,
				"url":      "postgresql://" + username + ":" + password + "@" + host + ":" + port + "/" + tenant.DatabaseName,
			}
		case "buckets":
			row, err := h.db.LiveServiceBucketClaim(ctx, environmentID, part.Service)
			if err != nil || claim.Phase(row.Phase) != claim.PhaseProvisioned {
				warnings = append(warnings, dotted+" is not provisioned yet")
				continue
			}
			allocation, err := h.db.LiveAllocation(ctx, row.ID)
			if err != nil {
				warnings = append(warnings, dotted+" has no live allocation")
				continue
			}
			data, err := h.secrets(ctx, "skali-platform", allocation.CredentialSecret)
			if err != nil {
				warnings = append(warnings, dotted+": reading the credential failed")
				continue
			}
			endpoint := allocation.Endpoint
			if audience == "local" {
				endpoint = "http://127.0.0.1:" + hostPort(bundle.S3NodePort)
			}
			outputs[dotted] = map[string]string{
				"endpoint":   endpoint,
				"name":       allocation.BucketName,
				"region":     allocation.Region,
				"access_key": string(data["access_key"]),
				"secret_key": string(data["secret_key"]),
			}
		}
	}
	return outputs, warnings
}
