import { useCallback, useEffect, useState, type FormEvent } from "react";

import {
  getEnergySettings,
  getStressObservability,
  getStressValidation,
  updateEnergySettings,
  type AdminEnergySettingsResponse,
  type StressObservability,
  type StressValidation,
} from "../../api/operations";
import type { Locale } from "../../i18n";
import { tp } from "../../i18n/pages";

interface Props {
  locale: Locale;
  schema?: string;
}

function fmtNum(v: number | undefined): string {
  if (v == null || !Number.isFinite(v)) return "—";
  return v.toFixed(2);
}

function verdictTone(verdict: string): "good" | "warn" | "danger" {
  if (verdict === "validated") return "good";
  if (verdict === "weak") return "warn";
  return "danger";
}

export function AdminEnergyTab({ locale, schema }: Props) {
  const [obs, setObs] = useState<StressObservability | null>(null);
  const [obsError, setObsError] = useState<string | null>(null);

  const [validation, setValidation] = useState<StressValidation | null>(null);
  const [valLoading, setValLoading] = useState(false);
  const [valError, setValError] = useState<string | null>(null);

  const [energy, setEnergy] = useState<AdminEnergySettingsResponse | null>(null);
  const [energyLoadError, setEnergyLoadError] = useState<string | null>(null);
  const [stressEnabled, setStressEnabled] = useState(false);
  const [beta, setBeta] = useState("0.8");
  const [zThreshold, setZThreshold] = useState("0.5");
  const [saving, setSaving] = useState(false);
  const [energyMsg, setEnergyMsg] = useState<{ tone: "good" | "danger"; text: string } | null>(null);

  const loadAll = useCallback(async () => {
    setObsError(null);
    setEnergyLoadError(null);
    await Promise.all([
      getStressObservability(30, schema)
        .then(setObs)
        .catch((error: unknown) =>
          setObsError(error instanceof Error ? error.message : tp(locale, "errorGeneric")),
        ),
      getEnergySettings(schema)
        .then((res) => {
          setEnergy(res);
          setEnergyLoadError(null);
          setStressEnabled(Boolean(res["energy.stress_drain_enabled"]));
          setBeta(String(res["energy.beta"]));
          setZThreshold(String(res["energy.z_threshold"]));
        })
        .catch((error: unknown) =>
          setEnergyLoadError(
            error instanceof Error ? error.message : tp(locale, "errorGeneric"),
          ),
        ),
    ]);
  }, [locale, schema]);

  useEffect(() => {
    const controller = new AbortController();
    getStressObservability(30, schema, controller.signal)
      .then((res) => {
        if (!controller.signal.aborted) setObs(res);
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setObsError(error instanceof Error ? error.message : tp(locale, "errorGeneric"));
        }
      });
    getEnergySettings(schema, controller.signal)
      .then((res) => {
        if (!controller.signal.aborted) {
          setEnergy(res);
          setEnergyLoadError(null);
          setStressEnabled(Boolean(res["energy.stress_drain_enabled"]));
          setBeta(String(res["energy.beta"]));
          setZThreshold(String(res["energy.z_threshold"]));
        }
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setEnergyLoadError(
            error instanceof Error ? error.message : tp(locale, "errorGeneric"),
          );
        }
      });
    return () => controller.abort();
  }, [locale, schema]);

  const handleRunValidation = async () => {
    setValLoading(true);
    setValError(null);
    try {
      setValidation(await getStressValidation(30, schema));
    } catch (error: unknown) {
      setValError(error instanceof Error ? error.message : tp(locale, "errorGeneric"));
    } finally {
      setValLoading(false);
    }
  };

  const handleSaveEnergy = async (event: FormEvent) => {
    event.preventDefault();
    setSaving(true);
    setEnergyMsg(null);
    try {
      await updateEnergySettings(
        {
          "energy.beta": beta,
          "energy.z_threshold": zThreshold,
          "energy.stress_drain_enabled": stressEnabled ? "true" : "false",
        },
        schema,
      );
      setEnergyMsg({ tone: "good", text: tp(locale, "saved") });
      await loadAll();
    } catch (error: unknown) {
      setEnergyMsg({
        tone: "danger",
        text: error instanceof Error ? error.message : tp(locale, "errorGeneric"),
      });
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
      {/* Stress observability */}
      <details className="panel panel--collapsible" open>
        <summary>{tp(locale, "adminStressObsTitle")}</summary>
        <div className="panel__body">
          {obsError ? <div className="notice notice--danger">{obsError}</div> : null}
          {obs ? (
            <>
              <div className={`notice notice--${obs.applied ? "warn" : "good"}`}>
                {obs.applied
                  ? tp(locale, "adminStressApplied")
                  : tp(locale, "adminStressObservedOnly")}
              </div>
              <div className="table-scroll">
                <table className="data-table">
                  <tbody>
                    <tr>
                      <td>Window</td>
                      <td>
                        {obs.from} → {obs.to} ({obs.window_days}d)
                      </td>
                    </tr>
                    <tr>
                      <td>{tp(locale, "adminEnergyEffectiveBeta")}</td>
                      <td>
                        <code>{obs.effective_beta}</code> (β={obs.beta}, z={obs.z_threshold})
                      </td>
                    </tr>
                    <tr>
                      <td>Load stats</td>
                      <td>
                        days={obs.distribution.days}, empty={obs.distribution.empty}, median=
                        {fmtNum(obs.distribution.load_median)}, p90=
                        {fmtNum(obs.distribution.load_p90)}, max={fmtNum(obs.distribution.load_max)}
                      </td>
                    </tr>
                    <tr>
                      <td>Validation</td>
                      <td>
                        <strong>{(obs.validation.verdict || "inconclusive").toUpperCase()}</strong>{" "}
                        <span className="subpage__muted">{obs.validation.reason}</span>
                      </td>
                    </tr>
                    <tr>
                      <td>Flags</td>
                      <td>
                        {Object.keys(obs.distribution.flag_counts ?? {}).length > 0 ? (
                          <div className="btn-row">
                            {Object.entries(obs.distribution.flag_counts).map(([k, v]) => (
                              <span key={k} className="pill">
                                {k}: {v}
                              </span>
                            ))}
                          </div>
                        ) : (
                          "—"
                        )}
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </>
          ) : (
            <p className="subpage__muted">{tp(locale, "loading")}</p>
          )}
        </div>
      </details>

      {/* Stress validation */}
      <details className="panel panel--collapsible" open>
        <summary>{tp(locale, "adminStressValTitle")}</summary>
        <div className="panel__body">
          <div className="btn-row">
            <button
              type="button"
              className="btn btn--primary btn--small"
              disabled={valLoading}
              onClick={() => void handleRunValidation()}
            >
              {valLoading ? tp(locale, "loading") : tp(locale, "adminStressValRun")}
            </button>
          </div>
          {valError ? <div className="notice notice--danger">{valError}</div> : null}
          {validation ? (
            <>
              <div className={`notice notice--${verdictTone(validation.verdict)}`}>
                <strong>{validation.verdict.toUpperCase()}</strong> — {validation.reason}
              </div>
              <div className="table-scroll">
                <table className="data-table">
                  <tbody>
                    <tr>
                      <td>Channel 1 (HRV)</td>
                      <td>
                        {validation.channel_1_hrv.r != null
                          ? `r = ${validation.channel_1_hrv.r.toFixed(3)} (n=${validation.channel_1_hrv.n})`
                          : `n=${validation.channel_1_hrv.n}`}
                      </td>
                    </tr>
                    <tr>
                      <td>Channel 2 (RHR)</td>
                      <td>
                        {validation.channel_2_rhr.r != null
                          ? `r = ${validation.channel_2_rhr.r.toFixed(3)} (n=${validation.channel_2_rhr.n})`
                          : `n=${validation.channel_2_rhr.n}`}
                      </td>
                    </tr>
                    <tr>
                      <td>Channel 3 (Sleep)</td>
                      <td>
                        votes {validation.channel_3_sleep.agreement_votes}/3 (n=
                        {validation.channel_3_sleep.n})
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </>
          ) : null}
        </div>
      </details>

      {/* Energy settings */}
      <details className="panel panel--collapsible">
        <summary>{tp(locale, "adminEnergyTitle")}</summary>
        <div className="panel__body">
          {energyLoadError ? (
            <div className="field">
              <div className="notice notice--danger">{energyLoadError}</div>
              <div className="btn-row">
                <button
                  type="button"
                  className="btn btn--small"
                  onClick={() => void loadAll()}
                >
                  {tp(locale, "retry")}
                </button>
              </div>
            </div>
          ) : null}
          <form className="field-grid" onSubmit={handleSaveEnergy}>
            <label className="field__checkbox">
              <input
                type="checkbox"
                checked={stressEnabled}
                disabled={!energy || saving}
                onChange={(event) => setStressEnabled(event.target.checked)}
              />
              {tp(locale, "adminEnergyStressEnabled")}
            </label>
            <div className="field-grid field-grid--two">
              <div className="field">
                <label className="field__label" htmlFor="cfg-energy-beta">
                  {tp(locale, "adminEnergyBeta")}
                </label>
                <input
                  id="cfg-energy-beta"
                  type="number"
                  step="0.01"
                  min={0}
                  max={5}
                  className="field__input"
                  value={beta}
                  disabled={!energy || saving}
                  onChange={(event) => setBeta(event.target.value)}
                />
              </div>
              <div className="field">
                <label className="field__label" htmlFor="cfg-energy-z">
                  {tp(locale, "adminEnergyZThreshold")}
                </label>
                <input
                  id="cfg-energy-z"
                  type="number"
                  step="0.05"
                  min={0}
                  max={3}
                  className="field__input"
                  value={zThreshold}
                  disabled={!energy || saving}
                  onChange={(event) => setZThreshold(event.target.value)}
                />
              </div>
            </div>
            <p className="subpage__muted">
              {tp(locale, "adminEnergyEffectiveBeta")}:{" "}
              <code>{energy ? energy.effective_beta : "—"}</code>
            </p>
            <div className="btn-row">
              <button
                type="submit"
                className="btn btn--primary btn--small"
                disabled={!energy || saving}
              >
                {saving ? tp(locale, "saving") : tp(locale, "save")}
              </button>
            </div>
            {energyMsg ? (
              <div className={`notice notice--${energyMsg.tone}`}>
                {energyMsg.text}
              </div>
            ) : null}
          </form>
        </div>
      </details>
    </>
  );
}
