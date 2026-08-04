import { expect, test } from "@playwright/test";

const sectionOrder = ["sleep", "yesterday", "recovery", "recommendation"];

test("uses the insight card width without changing reading order", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto("/?lang=en&fixture=normal");

  const sections = page.locator(".insight-card__sections article");
  await expect(sections).toHaveCount(4);
  expect(
    await sections.evaluateAll((articles) =>
      articles.map((article) => article.getAttribute("data-insight-section")),
    ),
  ).toEqual(
    sectionOrder,
  );

  const boxes = await sections.evaluateAll((articles) =>
    articles.map((article) => {
      const box = article.getBoundingClientRect();
      return { left: box.left, top: box.top, width: box.width };
    }),
  );

  expect(boxes[0].top).toBeCloseTo(boxes[1].top, 0);
  expect(boxes[2].top).toBeCloseTo(boxes[3].top, 0);
  expect(boxes[0].left).toBeCloseTo(boxes[2].left, 0);
  expect(boxes[1].left).toBeCloseTo(boxes[3].left, 0);
  expect(boxes[0].width).toBeGreaterThan(300);
});

test("stacks insight sections in one column on mobile", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/?lang=en&fixture=normal");

  const sections = page.locator(".insight-card__sections article");
  await expect(sections).toHaveCount(4);

  const boxes = await sections.evaluateAll((articles) =>
    articles.map((article) => {
      const box = article.getBoundingClientRect();
      return { left: box.left, top: box.top, width: box.width };
    }),
  );

  expect(boxes).toHaveLength(4);
  for (let index = 1; index < boxes.length; index += 1) {
    expect(boxes[index].left).toBeCloseTo(boxes[0].left, 0);
    expect(boxes[index].width).toBeCloseTo(boxes[0].width, 0);
    expect(boxes[index].top).toBeGreaterThan(boxes[index - 1].top);
  }
});
