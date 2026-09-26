import { expect, test } from "@playwright/test";

for (const locale of ["en", "ru", "sr"] as const) {
  for (const route of ["/", "/sleep", "/recovery", "/energy"] as const) {
    test(`${route} shows separate Server and AI Insights in ${locale} on mobile`, async ({ page }) => {
      await page.setViewportSize({ width: 390, height: 844 });
      await page.goto(`${route}?lang=${locale}&fixture=normal`);
      const pair = page.locator(".insight-pair");
      await expect(pair.locator(".insight-pair__card")).toHaveCount(2);
      await expect(pair.getByText("Server Insight")).toBeVisible();
      await expect(pair.getByText("AI Insight")).toBeVisible();
      const boxes = await pair.locator(".insight-pair__card").evaluateAll((cards) => cards.map((card) => {
        const box = card.getBoundingClientRect();
        return { x: box.x, y: box.y, width: box.width };
      }));
      expect(boxes[1].y).toBeGreaterThan(boxes[0].y);
      expect(Math.abs(boxes[1].width - boxes[0].width)).toBeLessThanOrEqual(1);
      const width = await page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, viewport: window.innerWidth }));
      expect(width.scroll).toBeLessThanOrEqual(width.viewport);
    });
  }
}

test("overall disagreement and alternative action remain fully readable beside Server Insight", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 960 });
  await page.goto("/?lang=ru&fixture=normal");
  const pair = page.locator(".today-hero .insight-pair");
  const cards = pair.locator(".insight-pair__card");
  await expect(cards).toHaveCount(2);
  await expect(cards.nth(1)).toContainText("знакомая спокойная активность");
  await expect(cards.nth(1)).toContainText("Выбери одну привычную спокойную активность");
  const boxes = await cards.evaluateAll((nodes) => nodes.map((node) => {
    const box = node.getBoundingClientRect();
    return { x: box.x, y: box.y, width: box.width };
  }));
  expect(Math.abs(boxes[0].y - boxes[1].y)).toBeLessThanOrEqual(1);
  expect(Math.abs(boxes[0].width - boxes[1].width)).toBeLessThanOrEqual(1);
});

test("server insight remains complete when AI is absent", async ({ page }) => {
  await page.goto("/?lang=en&fixture=partial");
  await expect(page.locator(".today-hero .insight-pair__card--server")).toBeVisible();
  await expect(page.locator(".today-hero .insight-pair__card--ai")).toHaveCount(0);
});
