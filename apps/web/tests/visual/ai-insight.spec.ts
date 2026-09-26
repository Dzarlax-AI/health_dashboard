import { expect, test } from "@playwright/test";

const insightLabels = {
  en: { server: "Server Insight", ai: "AI Insight" },
  ru: { server: "Инсайт сервера", ai: "AI-инсайт" },
  sr: { server: "Serverski uvid", ai: "AI uvid" },
} as const;

for (const route of ["/", "/sleep"] as const) {
  for (const query of ["?lang=en", "?lang=en&fixture=unknown"] as const) {
    test(`${route} requests live health data with ${query} in fixture-enabled builds`, async ({ page }) => {
      await page.route("**/api/**", (request) => request.fulfill({ status: 503, contentType: "application/json", body: "{}" }));
      const briefingRequest = page.waitForRequest((request) => request.url().includes("/api/health-briefing"));
      await page.goto(`${route}${query}`);
      await briefingRequest;
      await expect(page.locator(".insight-pair__card")).toHaveCount(0);
    });
  }
}

for (const locale of ["en", "ru", "sr"] as const) {
  for (const route of ["/", "/sleep", "/recovery", "/energy"] as const) {
    test(`${route} shows separate Server and AI Insights in ${locale} on mobile`, async ({ page }) => {
      await page.setViewportSize({ width: 390, height: 844 });
      await page.goto(`${route}?lang=${locale}&fixture=normal`);
      const pair = page.locator(".insight-pair");
      await expect(pair.locator(".insight-pair__card")).toHaveCount(2);
      await expect(pair.getByText(insightLabels[locale].server)).toBeVisible();
      await expect(pair.getByText(insightLabels[locale].ai)).toBeVisible();
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

test("partial-sleep-data AI-unavailable copy keeps explicit contrast on the Sleep hero", async ({ page }) => {
  await page.goto("/sleep?lang=en&fixture=partial");
  const note = page.locator(".sleep-hero .insight-pair__unavailable");
  await expect(note).toBeVisible();
  await expect(note).toHaveText("Sleep data are partial. AI Insight is unavailable here; the Server Insight remains the reliable view.");
  await expect(note).toHaveCSS("color", "rgb(247, 248, 255)");
  await expect(note).toHaveCSS("background-color", "rgba(13, 28, 54, 0.48)");
});

for (const theme of ["light", "dark"] as const) {
  for (const route of ["/", "/sleep", "/recovery", "/energy"] as const) {
    test(`${route} insight cards use readable text on their own surface in ${theme} mode`, async ({ page }) => {
      await page.emulateMedia({ colorScheme: theme });
      await page.setViewportSize({ width: 390, height: 844 });
      await page.goto(`${route}?lang=ru&fixture=normal`);

      const cards = page.locator(".insight-pair__card");
      await expect(cards).toHaveCount(2);
      for (const card of await cards.all()) {
        await expect(card).toHaveCSS("background-color", theme === "light" ? "rgb(255, 255, 255)" : "rgb(27, 34, 30)");
        await expect(card).toHaveCSS("color", theme === "light" ? "rgb(26, 26, 30)" : "rgb(241, 245, 242)");
      }
    });
  }
}

for (const route of ["/sleep", "/recovery", "/energy"] as const) {
  test(`${route} keeps a dark backdrop behind its light hero text`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(`${route}?lang=ru&fixture=normal`);
    const hero = page.locator(route === "/sleep" ? ".sleep-hero" : ".health-detail-hero");
    await expect(hero).toHaveCSS("background-image", /linear-gradient/);
  });
}
