import createClient from "openapi-fetch";

import type { Locale } from "../i18n";
import type { components, paths } from "./generated/schema";

export type DashboardResponse = components["schemas"]["DashboardResponse"];
export type HealthBriefingResponse = components["schemas"]["HealthBriefingResponse"];
export type AIBriefingResponse = components["schemas"]["AIBriefingResponse"];
export type ReadinessHistoryResponse = components["schemas"]["ReadinessHistoryResponse"];
export type EnergyHistoryResponse =
  | components["schemas"]["EnergyHistoryDayResponse"]
  | components["schemas"]["EnergyHistoryHourResponse"];
export type SessionResponse = components["schemas"]["SessionResponse"];

export class ClientApiError extends Error {
  readonly status: number;
  readonly retryable: boolean;

  constructor(status: number, message: string) {
    super(message);
    this.name = "ClientApiError";
    this.status = status;
    this.retryable = status >= 500;
  }
}

const client = createClient<paths>({
  baseUrl: "/",
  headers: {
    Accept: "application/json",
  },
});

function requireData<T>(
  data: T | undefined,
  error: unknown,
  response: Response,
): T {
  if (response.redirected || response.headers.get("content-type")?.includes("text/html")) {
    throw new ClientApiError(401, "Interactive authentication is required");
  }
  if (data === undefined) {
    const detail = typeof error === "string" ? error : response.statusText;
    throw new ClientApiError(response.status, detail || "Client API request failed");
  }
  return data;
}

export async function getDashboard(signal?: AbortSignal): Promise<DashboardResponse> {
  const { data, error, response } = await client.GET("/api/dashboard", { signal });
  return requireData(data, error, response);
}

export async function getHealthBriefing(
  locale: Locale,
  signal?: AbortSignal,
): Promise<HealthBriefingResponse> {
  const { data, error, response } = await client.GET("/api/health-briefing", {
    params: { query: { lang: locale } },
    signal,
  });
  return requireData(data, error, response);
}

export async function getAIBriefing(
  locale: Locale,
  signal?: AbortSignal,
): Promise<AIBriefingResponse> {
  const { data, error, response } = await client.GET("/api/ai-briefing", {
    params: { query: { lang: locale } },
    signal,
  });
  return requireData(data, error, response);
}

export async function getReadinessHistory(
  days = 30,
  signal?: AbortSignal,
): Promise<ReadinessHistoryResponse> {
  const { data, error, response } = await client.GET("/api/readiness-history", {
    params: { query: { days } },
    signal,
  });
  return requireData(data, error, response);
}

export async function getEnergyHistory(
  days = 14,
  signal?: AbortSignal,
): Promise<EnergyHistoryResponse> {
  const { data, error, response } = await client.GET("/api/energy-history", {
    params: { query: { granularity: "day", days } },
    signal,
  });
  return requireData(data, error, response);
}

export async function getSession(signal?: AbortSignal): Promise<SessionResponse> {
  const { data, error, response } = await client.GET("/api/session", { signal });
  return requireData(data, error, response);
}
