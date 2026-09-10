package httpapi

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/heridotlife/honryu/internal/app/adminapp"
	"github.com/heridotlife/honryu/internal/app/calibrationapp"
	"github.com/heridotlife/honryu/internal/app/campaignapp"
	"github.com/heridotlife/honryu/internal/app/clusterapp"
	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/app/lifecycleapp"
	"github.com/heridotlife/honryu/internal/app/metricsapp"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/app/quotaapp"
	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/app/tenantapp"
	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/domain/campaign"
	"github.com/heridotlife/honryu/internal/domain/clusterregistry"
	"github.com/heridotlife/honryu/internal/domain/compile"
	"github.com/heridotlife/honryu/internal/domain/digest"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/jmx"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/run"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/schedule"
	"github.com/heridotlife/honryu/internal/domain/tenant"
	"github.com/heridotlife/honryu/internal/domain/webhook"
	"github.com/heridotlife/honryu/internal/ports"
)

// errForbidden is returned by ownership checks; mapped to HTTP 403.
var errForbidden = errors.New("forbidden")

// badRequestErrors are client input/validation failures → HTTP 400.
var badRequestErrors = []error{
	project.ErrNameRequired, project.ErrNameTooLong, project.ErrOwnerRequired,
	project.ErrOwnerTooLong, project.ErrSIDInvalid, project.ErrSIDTooLong,
	scenario.ErrNameRequired, scenario.ErrNameTooLong, scenario.ErrProjectRequired,
	execution.ErrNameRequired, execution.ErrNameTooLong, execution.ErrProjectRequired,
	loadprofile.ErrScenarioRequired, loadprofile.ErrEnginesInvalid, loadprofile.ErrConcurrencyInvalid,
	loadprofile.ErrDurationInvalid, loadprofile.ErrNoScenarios,
	scenarioapp.ErrInvalidFilename, scenarioapp.ErrRequestsInvalid,
	// An unusable JMeter plan is the caller's file, not a server fault: the
	// import must say which of the three ways it was unusable.
	jmx.ErrMalformed, jmx.ErrNotJMX, jmx.ErrNoTestPlan,
	executionapp.ErrInvalidFilename, executionapp.ErrExecutionMismatch,
	executionapp.ErrScenarioNotInProject, executionapp.ErrEngineLimit,
	run.ErrNoScenarios, lifecycleapp.ErrNoTestFile,
	// A portable scenario deployed with no requests uploaded yet is a
	// configuration gap on the caller's side, the same as a native scenario
	// with no script (ErrNoTestFile above) -- not a server fault.
	compile.ErrRequestsRequired,
	// A scenario/engine pairing bzt's executors cannot honour (a portable
	// scenario under a script-only engine like k6, or a native artefact
	// under the wrong engine) is the caller's configuration, caught at
	// compile time inside Deploy -- 400 with the stated reason, not a 500.
	scenario.ErrEngineNeedsScript, scenario.ErrEnginePinned,
	tenant.ErrNameRequired, tenant.ErrNameTooLong, tenant.ErrNameInvalid,
	tenant.ErrDisplayNameRequired, tenant.ErrStatusInvalid,
	tenantapp.ErrCeilingInvalid,
	// An unsequenced batch is a sidecar contract violation, not a transient
	// failure -- retrying it would never succeed, so it must not read as one.
	ports.ErrUnsequencedBatch,
	tenantapp.ErrUnknownRole, tenantapp.ErrGlobalRoleScoped,
	schedule.ErrExecutionRequired, schedule.ErrKindInvalid, schedule.ErrFireAtRequired,
	schedule.ErrRecurrenceRequired, schedule.ErrRecurrenceInvalid, schedule.ErrWindowInvalid,
	adminapp.ErrScopeInvalid,
	campaign.ErrNameRequired, campaign.ErrWindowInvalid, campaign.ErrServicesRequired,
	campaign.ErrDuplicateService, campaign.ErrProjectRequired, campaign.ErrServiceExecutionInvalid,
	campaignapp.ErrServiceExecutionMismatch, campaignapp.ErrServiceProjectTenantMismatch,
	execution.ErrEngineUnknown,
	calibration.ErrCriterionRequired, calibration.ErrPodSizeRequired, calibration.ErrSeedQPSInvalid,
	calibration.ErrMaxQPSInvalid, calibration.ErrMaxStepsInvalid, calibration.ErrHoldInvalid,
	calibrationapp.ErrExecutionNotCalibration, calibrationapp.ErrEngineRequired,
	calibrationapp.ErrSourceScenarioNotBound,
	// A criterion outside Taurus's expression grammar is the caller's
	// input (phase 42's hotfix): rejected with the stated reason, not a
	// later run-time Config Error.
	calibration.ErrCriterionInvalid,
	digest.ErrPeriodInvalid,
	// Cluster registration input: a malformed entry or a non-self-contained
	// BYOC kubeconfig is the caller's, not a server fault.
	clusterregistry.ErrNameRequired, clusterregistry.ErrOriginUnknown,
	clusterregistry.ErrSecretRefRequired, clusterregistry.ErrNamespaceRequired,
	clusterregistry.ErrIngestURLRequired, clusterregistry.ErrSidecarImageRequired,
	clusterapp.ErrKubeconfigInvalid,
	// Webhook registration input (phase 40): a non-https, over-long, or
	// secret-over-long registration is the caller's endpoint to fix.
	webhook.ErrProjectRequired, webhook.ErrURLRequired, webhook.ErrURLNotHTTPS,
	webhook.ErrURLTooLong, webhook.ErrSecretTooLong,
}

// conflictErrors are state conflicts → HTTP 409.
var conflictErrors = []error{
	ports.ErrFileExists,
	scenarioapp.ErrScenarioInUse, scenarioapp.ErrScenarioNotPortable,
	projectapp.ErrProjectHasScenarios, projectapp.ErrProjectHasExecutions,
	run.ErrNotDeployed, run.ErrEnginesNotReady, run.ErrAlreadyRunning, run.ErrNotRunning,
	// Triggering engines that already finished: re-deploying is the fix, and
	// the trigger's bounded readiness wait must not retry it away either
	// (it is not a readiness error).
	run.ErrEnginesFinished,
	ports.ErrEnginesUnreachable, ports.ErrRunActive,
	// A pod pushing to an execution that is not running, or pushing for a run
	// that has ended, is a state conflict rather than a server fault -- and a
	// pod that outlived its run will do exactly this on every retry.
	metricsapp.ErrNoActiveRun, metricsapp.ErrStaleRun,
	// A well-formed Trigger call blocked by an active campaign's freeze is a
	// state conflict, not a client input error -- the same call would
	// succeed once the campaign's window closes.
	lifecycleapp.ErrCampaignFrozen,
	// Editing a campaign whose window has already opened: preparation-only
	// editing means the definition is frozen once live, and retrying the
	// same PUT will not make the window future again.
	campaignapp.ErrCampaignStarted,
	// Registering a duplicate cluster, or deleting one with an active run, are
	// state conflicts.
	ports.ErrClusterExists, clusterapp.ErrClusterInUse,
}

// respondError maps an application/domain error onto an HTTP status.
func respondError(w http.ResponseWriter, err error) {
	var probeErr *ports.ProbeError
	var finishedErr *lifecycleapp.EnginesFinishedError
	switch {
	case errors.Is(err, ports.ErrNotFound), errors.Is(err, ports.ErrObjectNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.As(err, &probeErr):
		// A cluster that is unreachable / unauthorized / under-privileged at
		// registration is a well-formed request the server cannot act on --
		// 422, with the probe's stated reason.
		writeError(w, http.StatusUnprocessableEntity, probeErr.Error())
	case errors.Is(err, errForbidden):
		writeError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, quotaapp.ErrOverQuota):
		// An over-quota reservation (Trigger under the tenant's ceiling) is a
		// well-formed request the ceiling refuses, not a server fault -- 429
		// with the quota condition named. The fixed message deliberately
		// replaces err.Error(), whose wrapped detail (tenant/cluster/ceiling)
		// is diagnostics, not a stable API contract; since phase 24 those
		// numbers ride in the details envelope instead, with the PUT
		// remediation an operator can act on. A bare sentinel (nothing typed
		// attached) falls back to the message-only envelope.
		var oqe *quotaapp.OverQuotaError
		if errors.As(err, &oqe) {
			hint := fmt.Sprintf("PUT /api/tenants/{tenant_id}/quota ceiling=%d", oqe.Ceiling)
			if oqe.NoQuotaConfigured {
				hint += "; no quota row exists for this tenant+cluster"
			}
			writeErrorDetails(w, http.StatusTooManyRequests, "reservation would exceed tenant quota", map[string]any{
				"tenant_id": oqe.TenantID,
				"cluster":   oqe.Cluster,
				"requested": oqe.Requested,
				"used":      oqe.Used,
				"ceiling":   oqe.Ceiling,
				"hint":      hint,
			})
		} else {
			writeError(w, http.StatusTooManyRequests, "reservation would exceed tenant quota")
		}
	case errors.As(err, &finishedErr):
		// Triggering engines that already ran and finished: re-deploying is
		// the fix. The verbatim conflict text stays the message (conflict
		// bodies pass it through), and since phase 24 the orphan count and
		// the purge-and-redeploy remediation ride in the details envelope.
		writeErrorDetails(w, http.StatusConflict, finishedErr.Error(), map[string]any{
			"orphaned_completions": finishedErr.Orphaned,
			"hint":                 "purge the execution and redeploy before triggering",
		})
	case matchesAny(err, conflictErrors):
		writeError(w, http.StatusConflict, err.Error())
	case matchesAny(err, badRequestErrors):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		slog.Error("httpapi: internal error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

func matchesAny(err error, sentinels []error) bool {
	for _, s := range sentinels {
		if errors.Is(err, s) {
			return true
		}
	}
	return false
}
