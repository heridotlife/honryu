package httpapi

import "net/http"

// APMLinkTemplate is one configured customer-APM link-out (phase 37). A
// deployment-wide fixture, not domain data: name is what a run page's button
// shows, URLTemplate keeps its {{placeholders}} intact so the FRONTEND
// substitutes per-run values -- this endpoint never sees run context, which
// is the point: the templates are the same for every run, so they are served
// once and cached client-side. Mirrors config.APMLinkTemplate; the wire type
// is httpapi's own, like session.Profile, so the adapter keeps importing
// nothing from config.
type APMLinkTemplate struct {
	Name        string
	URLTemplate string
}

// apmLinkResponse is the wire shape of GET /api/apm-links: the configured
// templates verbatim, placeholders included. Always an array (empty when the
// deployment configured none), so a caller never distinguishes null from
// "no link-outs".
type apmLinkResponse struct {
	Name        string `json:"name"`
	URLTemplate string `json:"url_template"`
}

// apmLinks serves the deployment's configured APM link-out templates. Any
// authenticated caller may read them: they carry no run data and no secrets
// (a URL shape the operator already chose to send to every operator's
// browser), and every run-page viewer needs them. Honryu propagates trace
// context but does not trace -- these link-outs ARE the APM depth.
func (h *handlers) apmLinks(w http.ResponseWriter, _ *http.Request) {
	out := make([]apmLinkResponse, 0, len(h.deps.APMLinks))
	for _, t := range h.deps.APMLinks {
		out = append(out, apmLinkResponse{Name: t.Name, URLTemplate: t.URLTemplate})
	}
	writeJSON(w, http.StatusOK, out)
}
