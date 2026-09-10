import { expect, test } from "@playwright/test";

const domainOrder = ["sleep", "recovery", "activity"];

test("keeps the three Today domains in server order on desktop", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto("/?lang=en&fixture=normal");

  const sections = page.locator(".today-insights__domain");
  await expect(sections).toHaveCount(3);
  expect(
    await sections.evaluateAll((domains) =>
      domains.map((domain) => new URL((domain as HTMLAnchorElement).href).pathname.slice(1)),
    ),
  ).toEqual(
    domainOrder,
  );

  const boxes = await sections.evaluateAll((articles) =>
    articles.map((article) => {
      const box = article.getBoundingClientRect();
      return { left: box.left, top: box.top, width: box.width };
    }),
  );

  expect(boxes[0].top).toBeCloseTo(boxes[1].top, 0);
  expect(boxes[1].top).toBeCloseTo(boxes[2].top, 0);
  expect(boxes[0].left).toBeLessThan(boxes[1].left);
  expect(boxes[1].left).toBeLessThan(boxes[2].left);
  expect(boxes[0].width).toBeGreaterThan(200);
});

test("stacks Today domains in one column on mobile", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/?lang=en&fixture=normal");

  const sections = page.locator(".today-insights__domain");
  await expect(sections).toHaveCount(3);

  const boxes = await sections.evaluateAll((articles) =>
    articles.map((article) => {
      const box = article.getBoundingClientRect();
      return { left: box.left, top: box.top, width: box.width };
    }),
  );

  expect(boxes).toHaveLength(3);
  for (let index = 1; index < boxes.length; index += 1) {
    expect(boxes[index].left).toBeCloseTo(boxes[0].left, 0);
    expect(boxes[index].width).toBeCloseTo(boxes[0].width, 0);
    expect(boxes[index].top).toBeGreaterThan(boxes[index - 1].top);
  }
});
