import { expect, test } from "@playwright/test";

for (const route of ["activity", "cardio", "recovery"] as const) {
  test(`${route} detail stays useful on mobile`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(`/${route}?lang=ru&fixture=normal`);

    await expect(page.locator(".health-detail-hero")).toBeVisible();
    await expect(page.getByRole("heading", { level: 2, name: "История" })).toBeVisible();
    await expect(page.locator(".health-detail-trend-card")).toHaveCount(3);
    await expect(page.locator(".health-detail-gauge")).toBeVisible();

    const width = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
    }));
    expect(width.scrollWidth).toBeLessThanOrEqual(width.clientWidth);
  });
}

test("complete history and empty data remain explicit", async ({ page }) => {
  await page.goto("/activity?lang=en&fixture=normal");
  await page.getByRole("button", { name: "All" }).click();
  await expect(page.getByRole("button", { name: "All" })).toHaveAttribute("aria-pressed", "true");
  await expect(page.locator(".trend-chart")).toHaveCount(3);

  await page.goto("/activity?lang=en&fixture=empty");
  await expect(page.locator(".health-detail-gauge")).toContainText("—");
  await expect(page.getByText("A personal baseline is still forming.")).toBeVisible();
  await expect(page.getByText("There is not enough history for this signal yet.")).toBeVisible();
});
