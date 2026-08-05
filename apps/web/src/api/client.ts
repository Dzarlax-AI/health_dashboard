import createClient from "openapi-fetch";

import type { Locale } from "../i18n";
import type { components, paths } from "./generated/schema";

export type DashboardResponse = components["schemas"]["DashboardResponse"];
export type HealthBriefingResponse = components["schemas"]["HealthBriefingResponse"];
export type AIBriefingResponse = components["schemas"]["AIBriefingResponse"];
export type DerivedMetricsResponse = components["schemas"]["DerivedMetricsResponse"];
export type MetricDataResponse = components["schemas"]["MetricDataResponse"];
export type MetricRangeResponse = components["schemas"]["MetricRangeResponse"];
export type ReadinessHistoryResponse = components["schemas"]["ReadinessHistoryResponse"];
export type SectionResponse = components["schemas"]["SectionResponse"];
export type EnergyHistoryDayResponse =
  components["schemas"]["EnergyHistoryDayResponse"];
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
  date?: string,
): Promise<AIBriefingResponse> {
  const { data, error, response } = await client.GET("/api/ai-briefing", {
    params: { query: { lang: locale, date } },
    signal,
  });
  return requireData(data, error, response);
}

export async function getSection(
  key: "sleep" | "cardio" | "activity" | "recovery",
  locale: Locale,
  signal?: AbortSignal,
): Promise<SectionResponse> {
  const { data, error, response } = await client.GET("/api/section/{key}", {
    params: { path: { key }, query: { lang: locale } },
    signal,
  });
  return requireData(data, error, response);
}

export async function getMetricData(
  metric: string,
  from: string,
  to: string,
  signal?: AbortSignal,
): Promise<MetricDataResponse> {
  const { data, error, response } = await client.GET("/api/metrics/data", {
    params: {
      query: {
        metric,
        from,
        to,
        bucket: "day",
        agg: "AVG",
        by_source: "0",
      },
    },
    signal,
  });
  return requireData(data, error, response);
}

export async function getMetricRange(
  metric: string,
  signal?: AbortSignal,
): Promise<MetricRangeResponse> {
  const { data, error, response } = await client.GET("/api/metrics/range", {
    params: { query: { metric } },
    signal,
  });
  return requireData(data, error, response);
}

export async function getDerivedMetrics(
  metric: "wake_time",
  from: string,
  to: string,
  signal?: AbortSignal,
): Promise<DerivedMetricsResponse> {
  const { data, error, response } = await client.GET("/api/derived-metrics", {
    params: { query: { metric, from, to } },
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
): Promise<EnergyHistoryDayResponse> {
  const { data, error, response } = await client.GET("/api/energy-history", {
    params: { query: { granularity: "day", days } },
    signal,
  });
  const history = requireData(data, error, response);
  if (history.granularity !== "day") {
    throw new ClientApiError(500, "Expected daily energy history");
  }
  return history;
}

export async function getSession(signal?: AbortSignal): Promise<SessionResponse> {
  const { data, error, response } = await client.GET("/api/session", { signal });
  return requireData(data, error, response);
}
