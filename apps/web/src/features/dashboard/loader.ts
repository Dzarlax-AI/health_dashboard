import {
  getAIBriefing,
  getDashboard,
  getEnergyHistory,
  getHealthBriefing,
  getReadinessHistory,
  getSession,
  type AIBriefingResponse,
  type DashboardResponse,
  type EnergyHistoryDayResponse,
  type HealthBriefingResponse,
  type ReadinessHistoryResponse,
  type SessionResponse,
} from "../../api/client";
import type { Locale } from "../../i18n";

export interface DashboardResources {
  briefing: HealthBriefingResponse;
  dashboard?: DashboardResponse;
  ai?: AIBriefingResponse;
  readinessHistory?: ReadinessHistoryResponse;
  energyHistory?: EnergyHistoryDayResponse;
  session?: SessionResponse;
  missing: string[];
}

export interface DashboardLoaders {
  briefing: typeof getHealthBriefing;
  dashboard: typeof getDashboard;
  ai: typeof getAIBriefing;
  readinessHistory: typeof getReadinessHistory;
  energyHistory: typeof getEnergyHistory;
  session: typeof getSession;
}

const defaultLoaders: DashboardLoaders = {
  briefing: getHealthBriefing,
  dashboard: getDashboard,
  ai: getAIBriefing,
  readinessHistory: getReadinessHistory,
  energyHistory: getEnergyHistory,
  session: getSession,
};

export async function loadDashboardResources(
  locale: Locale,
  signal?: AbortSignal,
  loaders: DashboardLoaders = defaultLoaders,
): Promise<DashboardResources> {
  const [briefing, dashboard, ai, readinessHistory, energyHistory, session] =
    await Promise.allSettled([
      loaders.briefing(locale, signal),
      loaders.dashboard(signal),
      loaders.ai(locale, signal),
      loaders.readinessHistory(30, signal),
      loaders.energyHistory(14, signal),
      loaders.session(signal),
    ]);

  if (briefing.status === "rejected") {
    throw briefing.reason;
  }

  const optional = { dashboard, ai, readinessHistory, energyHistory, session };
  const missing = Object.entries(optional)
    .filter(([, result]) => result.status === "rejected")
    .map(([name]) => name);

  return {
    briefing: briefing.value,
    dashboard: fulfilled(dashboard),
    ai: fulfilled(ai),
    readinessHistory: fulfilled(readinessHistory),
    energyHistory: fulfilled(energyHistory),
    session: fulfilled(session),
    missing,
  };
}

function fulfilled<T>(result: PromiseSettledResult<T>): T | undefined {
  return result.status === "fulfilled" ? result.value : undefined;
}
