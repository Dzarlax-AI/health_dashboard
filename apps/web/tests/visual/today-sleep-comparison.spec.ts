import { expect, test } from "@playwright/test";
import { fixtureResources } from "../../src/features/dashboard/fixtures";

test("Today explains the displayed sleep comparison locally until the next load", async ({ page }) => {
  const resources = fixtureResources("ru", "normal");
  let referenceMinutes = 420;
  let briefingReads = 0;
  let comparisonReads = 0;

  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/health-briefing") {
      briefingReads++;
      await route.fulfill({ json: {
        ...resources.briefing,
        sleep_duration_comparison: {
          wake_date: "2026-08-02", status: "available", duration_minutes: 390,
          reference_minutes: referenceMinutes, delta_minutes: 390 - referenceMinutes,
          eligible_nights: 18, window_start: "2026-07-03", window_end_exclusive: "2026-08-02",
          source: "Watch", rule_version: "sleep_duration_reference_v1",
        },
      } });
      return;
    }
    if (path === "/api/sleep/comparison") comparisonReads++;
    await route.fulfill({ status: 503, json: {} });
  });

  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/?lang=ru");
  const card = page.getByRole("region", { name: "Сравнение с личной нормой" });
  await expect(card).toContainText(/30.*меньше/i);
  referenceMinutes = 450;
  const toggle = card.locator("summary");
  await toggle.focus();
  await toggle.press("Enter");
  await expect(card.getByText(/Обычная длительность.*7 ч 0 мин/)).toBeVisible();
  await expect(card.getByText(/Сопоставимых ночей.*18/)).toBeVisible();
  expect(briefingReads).toBe(1);
  expect(comparisonReads).toBe(0);

  await page.reload();
  await expect(card).toContainText(/60.*меньше/i);
  await card.locator("summary").click();
  await expect(card.getByText(/Обычная длительность.*7 ч 30 мин/)).toBeVisible();
});
