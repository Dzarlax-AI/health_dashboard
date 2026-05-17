// Readiness redesign onboarding wizard — htmx fragment handlers.
//
// The wizard is a 7-step guided workflow built inside the Operations
// group on /admin (plan §6.2 runbook). Each step is a separate htmx
// fragment endpoint so the operator can re-render any step
// individually after they act on a previous step. State lives
// **purely in the database**: every step re-derives its "done /
// pending / warning" badge from row counts and schema probes, so an
// operator can close the page and come back at any time. No cookies,
// no session progress flags.
//
// Read-only steps: 1 (tenant check), 2 (config check), 3 (coverage
// preview), 4-plan (show backfill plan before run), 5 (verify),
// 7 (final pivot preview).
//
// Mutating steps: 4-run (POST → Phase 0 backfill) and 6-run (POST →
// chip-calibration recompute). Both reject `?schema=all` with 400 —
// recompute is per-tenant by design and the wizard mirrors that.

package ui

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"health-receiver/internal/storage"
)

// onboardingScope resolves and validates the tenant the wizard is
// targeting. The wizard works on one tenant at a time:
//
//   - `schema=all` is rejected with 400 (mutating and read-only).
//   - `schema=<name>` is accepted if it resolves.
//   - missing schema → fall back to the request's own tenant
//     (admin user's tenant). This is the safe default for both
//     legacy single-tenant mode and the multi-user "Current"
//     selector option whose value is the empty string.
//
// Returns the resolved scope or writes the relevant 4xx/5xx and
// returns nil.
func (h *Handler) onboardingScope(w http.ResponseWriter, r *http.Request) *operationalContractScope {
	schema := strings.TrimSpace(r.URL.Query().Get("schema"))
	if schema == "all" {
		http.Error(w, "schema=all is not allowed in the onboarding wizard: one tenant at a time", http.StatusBadRequest)
		return nil
	}
	// Empty schema means "use the request's own tenant" — same shape
	// as adminBackfill's default. resolveOperationalContractTenants
	// handles this path: scoped == "" → request tenant DB from ctx.
	scopes, scopeErr := h.resolveOperationalContractTenants(r, schema)
	if scopeErr != nil {
		http.Error(w, scopeErr.Error(), scopeErr.code)
		return nil
	}
	if len(scopes) != 1 {
		// Belt-and-suspenders — resolver guarantees exactly one with
		// non-"all" scope.
		http.Error(w, "unexpected scope size", http.StatusInternalServerError)
		return nil
	}
	return &scopes[0]
}

// ---- Step 1 — Tenant check -----------------------------------------------

func (h *Handler) fragmentOnboardingStep1(w http.ResponseWriter, r *http.Request) {
	scope := h.onboardingScope(w, r)
	if scope == nil {
		return
	}
	status, err := scope.db.LoadOnboardingTenantStatus(tenantLocalToday(h, scope.db, scope.schema))
	if err != nil {
		http.Error(w, "load tenant status: "+err.Error(), http.StatusInternalServerError)
		return
	}
	renderFragment(w, "admin-onboarding-step-1", struct {
		Lang    string
		Schema  string
		Status  *storage.OnboardingTenantStatus
		AnyRows bool
	}{
		Lang:    langFromRequest(r),
		Schema:  scope.schema,
		Status:  status,
		AnyRows: anySubScoreHasRows(status.SubScoreCounts),
	})
}

func anySubScoreHasRows(counts []storage.OnboardingSubScoreRowCounts) bool {
	for _, c := range counts {
		if c.TargetSnapshots > 0 || c.NaiveBaselines > 0 || c.FeatureSnapshots > 0 {
			return true
		}
	}
	return false
}

// ---- Step 2 — Config check -----------------------------------------------

func (h *Handler) fragmentOnboardingStep2(w http.ResponseWriter, r *http.Request) {
	scope := h.onboardingScope(w, r)
	if scope == nil {
		return
	}
	_, status := scope.db.LoadChronicLoadConfig()
	renderFragment(w, "admin-onboarding-step-2", struct {
		Lang   string
		Schema string
		Config storage.ChronicLoadConfigStatus
	}{Lang: langFromRequest(r), Schema: scope.schema, Config: status})
}

// ---- Step 3 — Coverage / base-rate preview --------------------------------

func (h *Handler) fragmentOnboardingStep3(w http.ResponseWriter, r *http.Request) {
	scope := h.onboardingScope(w, r)
	if scope == nil {
		return
	}
	asOf := tenantLocalToday(h, scope.db, scope.schema)
	summary, err := scope.db.LoadOnboardingCoverageSummary(asOf)
	if err != nil {
		http.Error(w, "load coverage: "+err.Error(), http.StatusInternalServerError)
		return
	}
	renderFragment(w, "admin-onboarding-step-3", struct {
		Lang    string
		Schema  string
		Summary *storage.OnboardingCoverageSummary
		// Per §6.2: the 15–30% Acute OR base rate band is the
		// operational sweet spot; outside it the chronic_acute_density
		// threshold likely needs a retune before backfill.
		BaseRatePct      string
		BaseRateInBand   bool
		BaseRateHasValue bool
	}{
		Lang:             langFromRequest(r),
		Schema:           scope.schema,
		Summary:          summary,
		BaseRatePct:      formatBaseRatePct(summary.AcuteORBaseRate),
		BaseRateInBand:   baseRateInBand(summary.AcuteORBaseRate),
		BaseRateHasValue: summary.AcuteORBaseRate != nil,
	})
}

func formatBaseRatePct(p *float64) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", *p*100)
}

func baseRateInBand(p *float64) bool {
	if p == nil {
		return false
	}
	return *p >= 0.15 && *p <= 0.30
}

// ---- Step 4 — Backfill plan + run ----------------------------------------

func (h *Handler) fragmentOnboardingStep4Plan(w http.ResponseWriter, r *http.Request) {
	scope := h.onboardingScope(w, r)
	if scope == nil {
		return
	}
	to := tenantLocalToday(h, scope.db, scope.schema)
	plan := buildOnboardingBackfillPlan(scope.schema, to)
	plan.Lang = langFromRequest(r)
	renderFragment(w, "admin-onboarding-step-4-plan", plan)
}

func (h *Handler) fragmentOnboardingStep4Run(w http.ResponseWriter, r *http.Request) {
	scope := h.onboardingScope(w, r)
	if scope == nil {
		return
	}
	to := tenantLocalToday(h, scope.db, scope.schema)
	plan := buildOnboardingBackfillPlan(scope.schema, to)
	plan.Lang = langFromRequest(r)

	type subRes struct {
		SubScore string `json:"sub_score"`
		Written  int    `json:"written"`
		Error    string `json:"error,omitempty"`
	}
	results := make([]subRes, 0, 4)

	// Run in the same dependency order as the manual runbook:
	// Recovery → Passive → Acute → Chronic. Each writer is
	// idempotent on its PKs so reruns after a partial failure are
	// safe; the wizard surfaces per-writer errors but does not stop
	// the chain because a later writer might depend on data the
	// earlier writers already produced on a prior call.
	type writer struct {
		name string
		fn   func(from, to string) (int, error)
	}
	writers := []writer{
		{storage.SubScoreRecoveryStability, scope.db.BackfillRecoveryStabilitySnapshots},
		{storage.SubScorePassiveEfficiency, scope.db.BackfillPassiveEfficiencySnapshots},
		{storage.SubScoreAcuteRisk, scope.db.BackfillAcuteRiskSnapshots},
		{storage.SubScoreChronicLoad, scope.db.BackfillChronicLoadSnapshots},
	}
	for _, wr := range writers {
		n, err := wr.fn(plan.From, plan.To)
		res := subRes{SubScore: wr.name, Written: n}
		if err != nil {
			res.Error = err.Error()
		}
		results = append(results, res)
	}

	renderFragment(w, "admin-onboarding-step-4-result", struct {
		Lang    string
		Schema  string
		Plan    onboardingBackfillPlan
		Results []subRes
	}{Lang: langFromRequest(r), Schema: scope.schema, Plan: plan, Results: results})
}

// onboardingBackfillPlan is the read-only summary the wizard renders
// in Step 4-plan BEFORE the operator clicks Run. Spelling out the
// date range, day count, force flag, and sub_score list is the
// "no magical button" requirement from the PR contract.
type onboardingBackfillPlan struct {
	Lang      string
	Schema    string
	From      string
	To        string
	Days      int
	Force     bool
	SubScores []string
}

func buildOnboardingBackfillPlan(schema, to string) onboardingBackfillPlan {
	// Default window: full eligible history from the §1 audit slice
	// start. Operators run this once per tenant onboarding; the
	// existing `/api/admin/readiness-redesign/backfill` endpoint
	// caps at 1825 days with `force=1`. We pick ~1 year as a sensible
	// default (much shorter than the cap) so the first run isn't a
	// surprise multi-hour job. Operators wanting deeper history can
	// re-run the backfill endpoint directly with force=1.
	toT, _ := time.Parse(isoDate, to)
	fromT := toT.AddDate(-1, 0, 0)
	// Inclusive day count — covers leap-year edges where AddDate(-1,0,0)
	// produces a 366-day span. The Step 4-plan UI is the operator's
	// "no surprise" preview, so this number must match what the
	// backfill writer will actually iterate over.
	days := int(toT.Sub(fromT).Hours()/24) + 1
	return onboardingBackfillPlan{
		Schema: schema,
		From:   fromT.Format(isoDate),
		To:     to,
		Days:   days,
		Force:  true,
		SubScores: []string{
			storage.SubScoreRecoveryStability,
			storage.SubScorePassiveEfficiency,
			storage.SubScoreAcuteRisk,
			storage.SubScoreChronicLoad,
		},
	}
}

const isoDate = "2006-01-02"

// ---- Step 5 — Verify ------------------------------------------------------

func (h *Handler) fragmentOnboardingStep5(w http.ResponseWriter, r *http.Request) {
	scope := h.onboardingScope(w, r)
	if scope == nil {
		return
	}
	status, err := scope.db.LoadOnboardingTenantStatus(tenantLocalToday(h, scope.db, scope.schema))
	if err != nil {
		http.Error(w, "load verify status: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Threshold echo verification — the load-bearing silent-failure
	// check from §6.2. The chronic writer stamps the breach +
	// acute_density thresholds it actually used onto every row's
	// `data_coverage`; sampling one row and comparing to the effective
	// config catches "operator changed settings but the writer used
	// defaults" (a class of bug that otherwise stays silent until a
	// chip-calibration recompute weeks later produces nonsense).
	// Scope the echo to the active epoch — see LoadChronicThresholdEcho
	// docs for why this matters. status.ActiveEpochID comes from the
	// same resolve call so the two stay consistent.
	echo, echoErr := scope.db.LoadChronicThresholdEcho(status.ActiveEpochID)
	_, cfg := scope.db.LoadChronicLoadConfig()
	renderFragment(w, "admin-onboarding-step-5", struct {
		Lang           string
		Schema         string
		Status         *storage.OnboardingTenantStatus
		Config         storage.ChronicLoadConfigStatus
		Echo           *storage.ChronicThresholdEcho
		EchoError      string
		ThresholdMatch bool
	}{
		Lang:           langFromRequest(r),
		Schema:         scope.schema,
		Status:         status,
		Config:         cfg,
		Echo:           echo,
		EchoError:      errString(echoErr),
		ThresholdMatch: echo.MatchesConfig(cfg.Effective),
	})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ---- Step 6 — Recompute chip calibrations ---------------------------------

func (h *Handler) fragmentOnboardingStep6Run(w http.ResponseWriter, r *http.Request) {
	scope := h.onboardingScope(w, r)
	if scope == nil {
		return
	}
	asOf := tenantLocalToday(h, scope.db, scope.schema)
	results, err := scope.db.RecomputeChipCalibrations(asOf)
	if err != nil {
		http.Error(w, "recompute: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Pre-format the nullable float fields here rather than in the
	// template. `printf "%.3f"` on a `*float64` renders the literal
	// pointer goo (`%!f(*float64=0x...)`), and template helpers that
	// dereference would still need a nil-guard, so it's clearer to
	// fold the formatting into a view struct and keep the template
	// dumb.
	type savedView struct {
		Status       string
		Method       string
		CutoffFmt    string
		P80Fmt       string
		BaseRateFmt  string
		NEligible    int
		NPositives   int
	}
	type resultView struct {
		SubScore   string
		TargetKind string
		Saved      *savedView
		Error      string
	}
	views := make([]resultView, 0, len(results))
	for _, r := range results {
		v := resultView{SubScore: r.SubScore, TargetKind: r.TargetKind, Error: r.Error}
		if r.Saved != nil {
			v.Saved = &savedView{
				Status:      r.Saved.Status,
				Method:      r.Saved.Method,
				CutoffFmt:   formatNullableFloat(r.Saved.Cutoff),
				P80Fmt:      formatNullableFloat(r.Saved.P80),
				BaseRateFmt: formatNullableFloat(r.Saved.BaseRate),
				NEligible:   r.Saved.NEligible,
				NPositives:  r.Saved.NPositives,
			}
		}
		views = append(views, v)
	}
	renderFragment(w, "admin-onboarding-step-6-result", struct {
		Lang    string
		Schema  string
		Results []resultView
	}{Lang: langFromRequest(r), Schema: scope.schema, Results: views})
}

func formatNullableFloat(p *float64) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprintf("%.3f", *p)
}

// ---- Step 7 — Final pivot preview ----------------------------------------

func (h *Handler) fragmentOnboardingStep7(w http.ResponseWriter, r *http.Request) {
	scope := h.onboardingScope(w, r)
	if scope == nil {
		return
	}
	// Reuse the existing operational-contract fragment scoped to one
	// tenant. The wizard's Step 7 is just "what the user sees after
	// onboarding" — no point inventing a parallel render path.
	q := r.URL.Query()
	q.Set("schema", scope.schema)
	if q.Get("days") == "" {
		q.Set("days", "7")
	}
	r.URL.RawQuery = q.Encode()
	h.fragmentAdminReadinessContract(w, r)
}
