package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/heridotlife/honryu/internal/domain/clusterregistry"
	"github.com/heridotlife/honryu/internal/domain/metrics"
)

// maxIngestBytes bounds a batch. A pod pushing every second sends far less than
// this; the cap exists so a misbehaving or hostile client cannot exhaust memory.
const maxIngestBytes = 8 << 20 // 8 MiB

// ingest accepts one engine pod's measurements.
//
// This is the only inbound path for results. Measurements are pushed by a
// sidecar rather than scraped, so the control plane never has to reach into a
// cluster -- which is what lets an execution run somewhere it could not address.
func (h *handlers) ingest(w http.ResponseWriter, r *http.Request) {
	var batch metrics.Batch
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxIngestBytes))
	if err := dec.Decode(&batch); err != nil {
		writeError(w, http.StatusBadRequest, "malformed batch")
		return
	}
	if batch.ExecutionID <= 0 {
		writeError(w, http.StatusBadRequest, "batch does not name an execution")
		return
	}

	// The body is decoded before credentials are checked because scoping a
	// cluster token needs to know which execution the batch claims.
	if code, msg := h.checkIngestCredentials(r.Context(), r, batch); code != 0 {
		writeError(w, code, msg)
		return
	}

	if err := h.deps.Metrics.Ingest(r.Context(), batch); err != nil {
		respondError(w, err)
		return
	}
	// Nothing to return: the pod has no use for a body, and a smaller response
	// matters when every pod posts every second.
	w.WriteHeader(http.StatusAccepted)
}

// checkIngestCredentials authenticates a push and, for cluster tokens, scopes
// it. It returns 0 when the push may proceed, else the HTTP status and message
// to answer with.
//
// Engine pods are not users: they hold no account and outlive no session, so
// they authenticate with a token rather than through the user AuthProvider. Two
// credentials exist, tried in order:
//
//  1. The deployment-wide engine token (Deps.IngestToken). Empty configured
//     token rejects everything, so a deployment that forgot to set one is
//     closed rather than open.
//  2. A per-cluster ingest token minted at BYOC registration. The presentation
//     is hashed and looked up; the token names a cluster, so the push is
//     additionally scoped to that cluster: the batch's execution must be one
//     routed there. A cluster token never speaks for the default fleet or for
//     a rival cluster's executions.
func (h *handlers) checkIngestCredentials(ctx context.Context, r *http.Request, batch metrics.Batch) (int, string) {
	presented, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return http.StatusUnauthorized, "invalid ingest credentials"
	}
	// Constant time, so a wrong token cannot be discovered a byte at a time.
	if h.deps.IngestToken != "" &&
		subtle.ConstantTimeCompare([]byte(presented), []byte(h.deps.IngestToken)) == 1 {
		return 0, ""
	}
	if h.deps.IngestTokens == nil || h.deps.ExecutionCluster == nil {
		return http.StatusUnauthorized, "invalid ingest credentials"
	}
	cluster, err := h.deps.IngestTokens.ClusterByIngestTokenHash(ctx, clusterregistry.HashToken(presented))
	if err != nil {
		return http.StatusUnauthorized, "invalid ingest credentials"
	}
	exe, err := h.deps.ExecutionCluster.GetExecution(ctx, batch.ExecutionID)
	if err != nil {
		return http.StatusForbidden, "ingest token is not valid for this execution"
	}
	// A fan-out execution (phase 88) runs on every cluster in its target
	// list, so a target cluster's token speaks for it exactly as the
	// single-cluster match always did; without this, engines in every
	// target cluster would be rejected on arrival and no fan-out run could
	// ever complete.
	if exe.Cluster != cluster.Name && !slices.Contains(exe.FanOutTargets, cluster.Name) {
		return http.StatusForbidden, "ingest token is not valid for this execution"
	}
	return 0, ""
}
