import { readFileSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";

const ui = new URL("../../../../internal/ui/", import.meta.url);
const template = readFileSync(new URL("templates/pages/metric_detail.html", ui), "utf8")
  .replaceAll("{{.MetricName}}", "step_count")
  .replace(/{{[^}]*}}/g, "");
const charts = readFileSync(new URL("static/charts.js", ui), "utf8");

const today = "2026-01-15";

async function serveStepPage(page: Page) {
  await page.route("**/metrics/step_count", (route) => route.fulfill({
    contentType: "text/html",
    body: `<script>
      class Chart { static register() {} destroy() {} }
      function $(id) { return document.getElementById(id); }
      function todayStr() { return '${today}'; }
      function daysAgoStr() { return '2025-12-17'; }
      function fmt2(value) { return String(value); }
    </script>${template}<script>
      const originalLoad = loadMetricChart;
      loadMetricChart = function(...args) {
        const result = originalLoad(...args);
        window.firstChartLoad ??= result;
        return result;
      };
    </script>`,
  }));
  await page.route("**/static/charts.js*", (route) => route.fulfill({ contentType: "text/javascript", body: charts }));
}

test("All plots the full daily step quantities once and preserves the source and short-range requests", async ({ page }) => {
  const requests: URL[] = [];
  let earliest = "2024-01-01";
  await serveStepPage(page);
  await page.route(/\/(fragments\/step-daily-series|api\/metrics\/(range|data))\?/, (route) => {
    const url = new URL(route.request().url());
    requests.push(url);
    return route.fulfill({ json: url.pathname.endsWith("/range")
      ? { min: earliest, max: today }
      : { bucket: "day", points: [{ date: earliest, qty: 0 }, { date: today, qty: 12 }] } });
  });

  await page.goto("/metrics/step_count");
  await expect(page.locator("#stats-row")).toContainText("Avg6");
  requests.length = 0;
  const all = page.locator('button[onclick="applyPresetAll()"]');
  await all.click();
  await expect(page.locator("#from")).toHaveValue(earliest);
  expect(requests.map((url) => url.pathname)).toEqual(["/fragments/step-daily-series"]);
  expect(requests[0].searchParams.has("from")).toBe(false);
  await expect(page.locator("#stats-row")).toContainText("Min0");

  await Promise.all([
    page.waitForResponse((response) => response.url().includes("by_source=1")),
    page.locator("#by-source-btn").click(),
  ]);
  await Promise.all([
    page.waitForResponse((response) => response.url().includes("/fragments/step-daily-series?")),
    page.locator("#by-source-btn").click(),
  ]);

  for (const [action, endpoint] of [
    [() => page.locator("#compare-btn").click(), "/api/metrics/data?"],
    [() => page.locator("#compare-btn").click(), "/fragments/step-daily-series?"],
    [() => page.locator("#agg").selectOption("MAX"), "/api/metrics/data?"],
    [() => page.locator("#agg").selectOption(""), "/fragments/step-daily-series?"],
  ] as const) {
    await Promise.all([page.waitForResponse((response) => response.url().includes(endpoint)), action()]);
  }

  earliest = "2026-01-07";
  const short = page.waitForResponse((response) => response.url().includes("/api/metrics/data?"));
  await all.click();
  const query = new URL((await short).url()).searchParams;
  expect(query.get("from")).toBe(earliest);
  expect(query.has("bucket")).toBe(false);
  await expect(page.locator("#from")).toHaveValue(earliest);
});

test("a delayed default result cannot replace the All chart", async ({ page }) => {
  await serveStepPage(page);
  let releaseDefault!: () => void;
  const deferred = new Promise<void>((resolve) => { releaseDefault = resolve; });
  await page.route("**/fragments/step-daily-series?*", async (route) => {
    const initial = new URL(route.request().url()).searchParams.has("from");
    if (initial) await deferred;
    await route.fulfill({ json: { bucket: "day", points: [
      { date: initial ? "2025-12-17" : "2024-01-01", qty: initial ? 99 : 12 },
    ] } });
  });
  const defaultRequested = page.waitForRequest((request) => new URL(request.url()).searchParams.has("from"));
  await page.goto("/metrics/step_count");
  await defaultRequested;
  await page.locator('button[onclick="applyPresetAll()"]').click();
  await expect(page.locator("#stats-row")).toContainText("Avg12");
  releaseDefault();
  await page.evaluate("window.firstChartLoad");
  await expect(page.locator("#from")).toHaveValue("2024-01-01");
  await expect(page.locator("#stats-row")).toContainText("Avg12");
});
