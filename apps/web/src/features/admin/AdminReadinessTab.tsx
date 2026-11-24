import { useCallback, useEffect, useState } from "react";

import {
  backfillReadiness,
  getCacheStatus,
  getReadinessConfig,
  getReadinessMonitoring,
  getReadinessOperationalContract,
  listChipCalibrations,
  recomputeChipCalibrations,
  updateReadinessConfig,
  type CacheStatus,
  type ChipCalibrationResponse,
  type ChronicLoadSettings,
  type MonitoringReport,
  type OperationalReport,
  type ReadinessBackfillResponse,
} from "../../api/operations";
import type { Locale } from "../../i18n";
import { tp } from "../../i18n/pages";
import { shiftDateIso, todayIso } from "../metrics/catalog";

interface Props {
  locale: Locale;
  schema?: string;
}

function statusPillClass(status: string | null | undefined): string {
  const s = (status ?? "").toLowerCase();
  if (s === "ok" || s === "calibrated" || s === "healthy" || s === "pass") {
    return "pill pill--good";
  }
  if (s === "warn" || s === "warning" || s === "partial" || s === "stale") {
    return "pill pill--warn";
  }
  if (s === "alert" || s === "fail" || s === "error" || s === "unhealthy") {
    return "pill pill--danger";
  }
  return "pill";
}

export function AdminReadinessTab({ locale, schema }: Props) {
  const today = todayIso();

  // Operational contract
  const [contract, setContract] = useState<OperationalReport | null>(null);
  const [contractError, setContractError] = useState<string | null>(null);
  const [contractLoading, setContractLoading] = useState(false);

  // Monitoring
  const [monitoring, setMonitoring] = useState<MonitoringReport | null>(null);
  const [monitoringError, setMonitoringError] = useState<string | null>(null);
  const [monitoringLoading, setMonitoringLoading] = useState(false);

  // Chronic load config
  const [config, setConfig] = useState<ChronicLoadSettings | null>(null);
  const [minAcuteDensity, setMinAcuteDensity] = useState("");
  const [minBreachDays, setMinBreachDays] = useState("");
  const [configMsg, setConfigMsg] = useState<{ tone: "good" | "danger"; text: string } | null>(null);

  // Backfill
  const [bfFrom, setBfFrom] = useState(() => shiftDateIso(today, -89));
  const [bfTo, setBfTo] = useState(() => today);
  const [bfSubScore, setBfSubScore] = useState("all");
  const [bfForce, setBfForce] = useState(false);
  const [bfBusy, setBfBusy] = useState(false);
  const [bfResult, setBfResult] = useState<ReadinessBackfillResponse | null>(null);
  const [bfError, setBfError] = useState<string | null>(null);

  // Chip calibrations
  const [chips, setChips] = useState<ChipCalibrationResponse | null>(null);
  const [chipsBusy, setChipsBusy] = useState(false);
  const [chipsError, setChipsError] = useState<string | null>(null);

  // Guided Wizard Step 1 (schema & cache health from unified /api/admin/status)
  const [wizardCache, setWizardCache] = useState<CacheStatus | null>(null);
  const [wizardBusy, setWizardBusy] = useState(false);

  const loadContract = useCallback(async () => {
    setContractLoading(true);
    setContractError(null);
    try {
      setContract(await getReadinessOperationalContract(14, schema));
    } catch (error: unknown) {
      setContractError(error instanceof Error ? error.message : tp(locale, "errorGeneric"));
    } finally {
      setContractLoading(false);
    }
  }, [locale, schema]);

  const loadMonitoring = useCallback(async () => {
    setMonitoringLoading(true);
    setMonitoringError(null);
    try {
      setMonitoring(await getReadinessMonitoring(schema));
    } catch (error: unknown) {
      setMonitoringError(error instanceof Error ? error.message : tp(locale, "errorGeneric"));
    } finally {
      setMonitoringLoading(false);
    }
  }, [locale, schema]);

  const loadConfig = useCallback(async () => {
    try {
      const res = await getReadinessConfig(schema);
      setConfig(res);
      setMinAcuteDensity(String(res.chronic_load_config.effective.min_acute_density));
      setMinBreachDays(String(res.chronic_load_config.effective.min_breach_days));
    } catch {
      // ignore
    }
  }, [schema]);

  const loadChips = useCallback(async () => {
    setChipsError(null);
    try {
      setChips(await listChipCalibrations(schema));
    } catch (error: unknown) {
      setChipsError(error instanceof Error ? error.message : tp(locale, "errorGeneric"));
    }
  }, [locale, schema]);

  useEffect(() => {
    const controller = new AbortController();
    getReadinessOperationalContract(14, schema, controller.signal)
      .then((res) => {
        if (!controller.signal.aborted) setContract(res);
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setContractError(error instanceof Error ? error.message : tp(locale, "errorGeneric"));
        }
      });
    getReadinessConfig(schema, controller.signal)
      .then((res) => {
        if (!controller.signal.aborted) {
          setConfig(res);
          setMinAcuteDensity(String(res.chronic_load_config.effective.min_acute_density));
          setMinBreachDays(String(res.chronic_load_config.effective.min_breach_days));
        }
      })
      .catch(() => undefined);
    listChipCalibrations(schema, controller.signal)
      .then((res) => {
        if (!controller.signal.aborted) setChips(res);
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setChipsError(error instanceof Error ? error.message : tp(locale, "errorGeneric"));
        }
      });
    return () => controller.abort();
  }, [locale, schema]);

  const handleSaveConfig = async () => {
    setConfigMsg(null);
    try {
      const res = await updateReadinessConfig(
        {
          "chronic_load.min_acute_density": Number(minAcuteDensity),
          "chronic_load.min_breach_days": Number(minBreachDays),
        },
        schema,
      );
      setConfig(res);
      setConfigMsg({ tone: "good", text: tp(locale, "saved") });
    } catch (error: unknown) {
      setConfigMsg({
        tone: "danger",
        text: error instanceof Error ? error.message : tp(locale, "errorGeneric"),
      });
    }
  };

  const handleRunBackfill = async (rangeOverride?: { from: string; to: string }) => {
    setBfBusy(true);
    setBfError(null);
    try {
      const res = await backfillReadiness(
        {
          from: rangeOverride?.from ?? bfFrom,
          to: rangeOverride?.to ?? bfTo,
          subScore: bfSubScore,
          force: bfForce,
        },
        schema,
      );
      setBfResult(res);
    } catch (error: unknown) {
      setBfError(error instanceof Error ? error.message : tp(locale, "errorGeneric"));
    } finally {
      setBfBusy(false);
    }
  };

  const handleRecomputeChips = async () => {
    setChipsBusy(true);
    setChipsError(null);
    try {
      setChips(await recomputeChipCalibrations(schema));
    } catch (error: unknown) {
      setChipsError(error instanceof Error ? error.message : tp(locale, "errorGeneric"));
    } finally {
      setChipsBusy(false);
    }
  };

  const handleWizardRunChecks = async () => {
    setWizardBusy(true);
    try {
      const [status] = await Promise.all([
        getCacheStatus(schema).catch(() => null),
        loadConfig(),
        loadChips(),
        loadContract(),
        loadMonitoring(),
      ]);
      setWizardCache(status);
    } finally {
      setWizardBusy(false);
    }
  };

  return (
    <>
      {/* Operational contract */}
      <details className="panel panel--collapsible" open>
        <summary>{tp(locale, "adminContractTitle")}</summary>
        <div className="panel__body">
          <div className="btn-row">
            <button
              type="button"
              className="btn btn--small"
              disabled={contractLoading}
              onClick={() => void loadContract()}
            >
              {tp(locale, "refresh")}
            </button>
            {contract ? (
              <span className="subpage__muted">
                {contract.from} → {contract.to} ({contract.rows?.length ?? 0})
              </span>
            ) : null}
          </div>
          {contractError ? (
            <div className="notice notice--danger">{contractError}</div>
          ) : null}
          {contract && (contract.rows?.length ?? 0) > 0 ? (
            <div className="table-scroll">
              <table className="data-table">
                <thead>
                  <tr>
                    <th>Date</th>
                    <th>SubScore</th>
                    <th>Target</th>
                    <th>Baseline</th>
                    <th>Calibration</th>
                    <th className="is-num">Predicted</th>
                    <th className="is-num">Cutoff</th>
                    <th>Eligible</th>
                  </tr>
                </thead>
                <tbody>
                  {(contract.rows ?? []).slice(0, 60).map((row, i) => (
                    <tr key={`${row.tenant}-${row.Date}-${row.SubScore}-${i}`}>
                      <td>
                        <code>{row.Date}</code>
                      </td>
                      <td>
                        <code>{row.SubScore}</code>
                      </td>
                      <td>{row.TargetKind}</td>
                      <td>{row.BaselineKind}</td>
                      <td>
                        <span className={statusPillClass(row.CalibrationStatus)}>
                          {row.CalibrationStatus || "—"}
                        </span>
                      </td>
                      <td className="is-num">
                        {row.PredictedValue == null ? "—" : row.PredictedValue.toFixed(2)}
                      </td>
                      <td className="is-num">
                        {row.Cutoff == null ? "—" : row.Cutoff.toFixed(2)}
                      </td>
                      <td>
                        {row.TargetEligible == null
                          ? "—"
                          : row.TargetEligible
                            ? "yes"
                            : row.TargetEligibilityReason || "no"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : contract ? (
            <p className="subpage__muted">{tp(locale, "noData")}</p>
          ) : null}
        </div>
      </details>

      {/* Monitoring */}
      <details className="panel panel--collapsible">
        <summary>{tp(locale, "adminMonitoringTitle")}</summary>
        <div className="panel__body">
          <div className="btn-row">
            <button
              type="button"
              className="btn btn--small"
              disabled={monitoringLoading}
              onClick={() => void loadMonitoring()}
            >
              {tp(locale, "refresh")}
            </button>
          </div>
          {monitoringError ? (
            <div className="notice notice--danger">{monitoringError}</div>
          ) : null}
          {monitoring && (monitoring.rows?.length ?? 0) > 0 ? (
            (monitoring.rows ?? []).map((row) => (
              <div key={row.tenant} className="field">
                <div className="btn-row">
                  <strong>{row.tenant}</strong>
                  <span className={statusPillClass(row.OverallStatus)}>
                    {row.OverallStatus}
                  </span>
                  <span className="subpage__muted">as of {row.AsOfDate}</span>
                </div>
                {(row.CoverageRows?.length ?? 0) > 0 ? (
                  <div className="table-scroll">
                    <table className="data-table">
                      <thead>
                        <tr>
                          <th>SubScore</th>
                          <th>Target</th>
                          <th className="is-num">Eligible</th>
                          <th className="is-num">Floor</th>
                          <th>Status</th>
                          <th>Top reason</th>
                        </tr>
                      </thead>
                      <tbody>
                        {(row.CoverageRows ?? []).map((cov) => (
                          <tr key={`${cov.SubScore}-${cov.TargetKind}`}>
                            <td>
                              <code>{cov.SubScore}</code>
                            </td>
                            <td>{cov.TargetKind}</td>
                            <td className="is-num">
                              {cov.Eligible}/{cov.Rows} ({cov.EligiblePct.toFixed(1)}%)
                            </td>
                            <td className="is-num">{cov.FloorPct.toFixed(0)}%</td>
                            <td>
                              <span className={statusPillClass(cov.Status)}>
                                {cov.Status}
                              </span>
                            </td>
                            <td>{cov.TopReason || "—"}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                ) : null}
                {(row.SourceEpochAlerts?.length ?? 0) > 0 ? (
                  <div className="field">
                    {(row.SourceEpochAlerts ?? []).map((alert) => (
                      <div key={alert.EpochID} className="notice notice--warn">
                        {alert.Kind}: {alert.Message} ({alert.StartDate} → {alert.EndDate})
                      </div>
                    ))}
                  </div>
                ) : null}
              </div>
            ))
          ) : monitoring ? (
            <p className="subpage__muted">{tp(locale, "noData")}</p>
          ) : null}
        </div>
      </details>

      {/* Operations: Chronic load config, Readiness backfill, Chip calibrations */}
      <details className="panel panel--collapsible">
        <summary>{tp(locale, "adminReadinessOpsTitle")}</summary>
        <div className="panel__body">
          <div className="field">
            <strong>{tp(locale, "adminChronicConfigTitle")}</strong>
            <div className="field-grid field-grid--two">
              <div className="field">
                <label className="field__label" htmlFor="cfg-chronic-density">
                  {tp(locale, "adminChronicDensity")}
                </label>
                <input
                  id="cfg-chronic-density"
                  type="number"
                  min={1}
                  className="field__input"
                  value={minAcuteDensity}
                  onChange={(event) => setMinAcuteDensity(event.target.value)}
                />
              </div>
              <div className="field">
                <label className="field__label" htmlFor="cfg-chronic-breach">
                  {tp(locale, "adminChronicBreach")}
                </label>
                <input
                  id="cfg-chronic-breach"
                  type="number"
                  min={1}
                  className="field__input"
                  value={minBreachDays}
                  onChange={(event) => setMinBreachDays(event.target.value)}
                />
              </div>
            </div>
            <div className="btn-row">
              <button
                type="button"
                className="btn btn--primary btn--small"
                onClick={() => void handleSaveConfig()}
              >
                {tp(locale, "save")}
              </button>
              {config ? (
                <span className="field__hint">
                  defaults: {config.chronic_load_config.defaults.min_acute_density} /{" "}
                  {config.chronic_load_config.defaults.min_breach_days}
                </span>
              ) : null}
            </div>
            {configMsg ? (
              <div className={`notice notice--${configMsg.tone}`}>
                {configMsg.text}
              </div>
            ) : null}
          </div>

          <hr style={{ border: 0, borderTop: "1px solid var(--border)" }} />

          <div className="field">
            <strong>{tp(locale, "adminReadinessBackfillTitle")}</strong>
            <div className="field-grid field-grid--two">
              <div className="field">
                <label className="field__label" htmlFor="rd-bf-from">
                  {tp(locale, "adminReadinessFrom")}
                </label>
                <input
                  id="rd-bf-from"
                  type="date"
                  className="field__input"
                  value={bfFrom}
                  onChange={(event) => setBfFrom(event.target.value)}
                />
              </div>
              <div className="field">
                <label className="field__label" htmlFor="rd-bf-to">
                  {tp(locale, "adminReadinessTo")}
                </label>
                <input
                  id="rd-bf-to"
                  type="date"
                  className="field__input"
                  value={bfTo}
                  onChange={(event) => setBfTo(event.target.value)}
                />
              </div>
            </div>
            <div className="field__row-inline">
              <label className="field__label" htmlFor="rd-bf-sub">
                {tp(locale, "adminReadinessSubScore")}
              </label>
              <select
                id="rd-bf-sub"
                className="field__select"
                value={bfSubScore}
                onChange={(event) => setBfSubScore(event.target.value)}
              >
                <option value="all">all</option>
                <option value="acute_risk">acute_risk</option>
                <option value="chronic_load">chronic_load</option>
                <option value="recovery_stability">recovery_stability</option>
                <option value="passive_efficiency">passive_efficiency</option>
              </select>
              <label className="field__checkbox">
                <input
                  type="checkbox"
                  checked={bfForce}
                  onChange={(event) => setBfForce(event.target.checked)}
                />
                {tp(locale, "adminReadinessForce")}
              </label>
              <button
                type="button"
                className="btn btn--primary btn--small"
                disabled={bfBusy}
                onClick={() => void handleRunBackfill()}
              >
                {bfBusy ? tp(locale, "loading") : tp(locale, "adminReadinessBackfillRun")}
              </button>
            </div>
            {bfError ? <div className="notice notice--danger">{bfError}</div> : null}
            {bfResult ? (
              <div className="notice notice--good">
                {bfResult.from} → {bfResult.to} ({bfResult.days}d):{" "}
                {Object.entries(bfResult.sub_scores)
                  .map(([k, v]) => `${k}=${v.written}${v.error ? ` (${v.error})` : ""}`)
                  .join(", ")}
              </div>
            ) : null}
          </div>

          <hr style={{ border: 0, borderTop: "1px solid var(--border)" }} />

          <div className="field">
            <div className="panel__header">
              <strong>{tp(locale, "adminChipRecomputeTitle")}</strong>
              <div className="btn-row">
                <button
                  type="button"
                  className="btn btn--small"
                  onClick={() => void loadChips()}
                >
                  {tp(locale, "refresh")}
                </button>
                <button
                  type="button"
                  className="btn btn--primary btn--small"
                  disabled={chipsBusy}
                  onClick={() => void handleRecomputeChips()}
                >
                  {tp(locale, "adminChipRecomputeBtn")}
                </button>
              </div>
            </div>
            {chipsError ? <div className="notice notice--danger">{chipsError}</div> : null}
            {chips && (chips.rows?.length ?? 0) > 0 ? (
              <div className="table-scroll">
                <table className="data-table">
                  <thead>
                    <tr>
                      <th>SubScore</th>
                      <th>Target</th>
                      <th>Status</th>
                      <th className="is-num">Eligible</th>
                      <th className="is-num">BaseRate</th>
                      <th className="is-num">Cutoff</th>
                    </tr>
                  </thead>
                  <tbody>
                    {(chips.rows ?? []).map((row, idx) => (
                      <tr key={`${row.SubScore}-${row.TargetKind}-${idx}`}>
                        <td>
                          <code>{row.SubScore}</code>
                        </td>
                        <td>{row.TargetKind}</td>
                        <td>
                          <span className={statusPillClass(row.Status)}>
                            {row.Status}
                          </span>
                        </td>
                        <td className="is-num">
                          {row.NPositives}/{row.NEligible}
                        </td>
                        <td className="is-num">
                          {row.BaseRate == null ? "—" : `${(row.BaseRate * 100).toFixed(1)}%`}
                        </td>
                        <td className="is-num">
                          {row.Cutoff == null ? "—" : row.Cutoff.toFixed(2)}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : null}
          </div>
        </div>
      </details>

      {/* Guided Onboarding Wizard (100% Unified JSON API) */}
      <details className="panel panel--collapsible">
        <summary>{tp(locale, "adminWizardTitle")}</summary>
        <div className="panel__body">
          <p className="subpage__muted">{tp(locale, "adminWizardDesc")}</p>
          <div className="btn-row">
            <button
              type="button"
              className="btn btn--primary btn--small"
              disabled={wizardBusy}
              onClick={() => void handleWizardRunChecks()}
            >
              {wizardBusy ? tp(locale, "loading") : tp(locale, "adminWizardLoadAll")}
            </button>
          </div>

          <div className="wizard-step">
            <div className="wizard-step__header">
              <span className="wizard-step__title">{tp(locale, "adminWizardStep1")}</span>
              <button
                type="button"
                className="btn btn--small"
                onClick={() => void getCacheStatus(schema).then(setWizardCache)}
              >
                {tp(locale, "refresh")}
              </button>
            </div>
            {wizardCache ? (
              <div className="btn-row">
                <span
                  className={
                    wizardCache.redesign_storage.healthy
                      ? "pill pill--good"
                      : "pill pill--danger"
                  }
                >
                  {wizardCache.redesign_storage.healthy
                    ? tp(locale, "adminHealthy")
                    : wizardCache.redesign_storage.error || tp(locale, "adminUnhealthy")}
                </span>
                <span className="subpage__muted">
                  daily_scores: {wizardCache.daily_scores.rows} ({wizardCache.daily_scores.oldest || "—"} → {wizardCache.daily_scores.newest || "—"})
                </span>
              </div>
            ) : (
              <span className="subpage__muted">—</span>
            )}
          </div>

          <div className="wizard-step">
            <div className="wizard-step__header">
              <span className="wizard-step__title">{tp(locale, "adminWizardStep2")}</span>
              <button
                type="button"
                className="btn btn--small"
                onClick={() => void loadConfig()}
              >
                {tp(locale, "refresh")}
              </button>
            </div>
            {config ? (
              <div className="btn-row">
                <span className="pill pill--good">
                  density={config.chronic_load_config.effective.min_acute_density}, breach_days=
                  {config.chronic_load_config.effective.min_breach_days}
                </span>
                {config.chronic_load_config.matches_defaults ? (
                  <span className="field__hint">defaults</span>
                ) : null}
              </div>
            ) : (
              <span className="subpage__muted">—</span>
            )}
          </div>

          <div className="wizard-step">
            <div className="wizard-step__header">
              <span className="wizard-step__title">{tp(locale, "adminWizardStep3")}</span>
              <button
                type="button"
                className="btn btn--primary btn--small"
                disabled={bfBusy}
                onClick={() =>
                  void handleRunBackfill({
                    from: shiftDateIso(today, -89),
                    to: today,
                  })
                }
              >
                {bfBusy ? tp(locale, "loading") : tp(locale, "adminReadinessBackfillRun")}
              </button>
            </div>
            {bfResult ? (
              <span className="subpage__muted">
                {bfResult.from} → {bfResult.to}:{" "}
                {Object.entries(bfResult.sub_scores)
                  .map(([k, v]) => `${k}=${v.written}`)
                  .join(", ")}
              </span>
            ) : (
              <span className="subpage__muted">—</span>
            )}
          </div>

          <div className="wizard-step">
            <div className="wizard-step__header">
              <span className="wizard-step__title">{tp(locale, "adminWizardStep4")}</span>
              <button
                type="button"
                className="btn btn--primary btn--small"
                disabled={chipsBusy}
                onClick={() => void handleRecomputeChips()}
              >
                {tp(locale, "adminChipRecomputeBtn")}
              </button>
            </div>
            <span className="subpage__muted">
              {chips ? `${chips.rows?.length ?? 0} calibrations` : "—"}
            </span>
          </div>

          <div className="wizard-step">
            <div className="wizard-step__header">
              <span className="wizard-step__title">{tp(locale, "adminWizardStep5")}</span>
              <button
                type="button"
                className="btn btn--small"
                onClick={() => {
                  void loadContract();
                  void loadMonitoring();
                }}
              >
                {tp(locale, "refresh")}
              </button>
            </div>
            <div className="btn-row">
              {contract ? (
                <span className="pill">contract rows: {contract.rows?.length ?? 0}</span>
              ) : null}
              {monitoring?.rows?.[0] ? (
                <span className={statusPillClass(monitoring.rows[0].OverallStatus)}>
                  monitoring: {monitoring.rows[0].OverallStatus}
                </span>
              ) : null}
            </div>
          </div>
        </div>
      </details>
    </>
  );
}
