import { expect, test, type Page } from "@playwright/test";

const widths = [320, 375, 768, 1024, 1440] as const;
const locales = ["en", "ru", "sr"] as const;

async function ringGeometry(page: Page) {
  return page.locator("[data-readiness-ring]").evaluate((ring) => {
    const frame = ring.querySelector<HTMLElement>("[data-gauge-frame]");
    const svg = ring.querySelector<SVGElement>("[data-gauge-svg]");
    const content = ring.querySelector<HTMLElement>("[data-gauge-content]");
    if (!frame || !svg || !content) {
      throw new Error("Readiness ring geometry elements are missing");
    }
    const frameBox = frame.getBoundingClientRect();
    const svgBox = svg.getBoundingClientRect();
    const contentBox = content.getBoundingClientRect();
    return {
      frame: {
        x: frameBox.x,
        y: frameBox.y,
        width: frameBox.width,
        height: frameBox.height,
      },
      svgCenter: {
        x: svgBox.x + svgBox.width / 2,
        y: svgBox.y + svgBox.height / 2,
      },
      contentCenter: {
        x: contentBox.x + contentBox.width / 2,
        y: contentBox.y + contentBox.height / 2,
      },
      documentWidth: document.documentElement.scrollWidth,
      viewportWidth: window.innerWidth,
    };
  });
}

for (const width of widths) {
  for (const locale of locales) {
    test(`ring is circular and centered at ${width}px in ${locale}`, async ({ page }) => {
      await page.setViewportSize({ width, height: 1100 });
      await page.emulateMedia({ reducedMotion: "reduce" });
      await page.goto(`/?lang=${locale}&fixture=normal`);
      const geometry = await ringGeometry(page);
      const frameCenter = {
        x: geometry.frame.x + geometry.frame.width / 2,
        y: geometry.frame.y + geometry.frame.height / 2,
      };

      expect(Math.abs(geometry.frame.width - geometry.frame.height)).toBeLessThanOrEqual(0.5);
      expect(Math.abs(geometry.svgCenter.x - frameCenter.x)).toBeLessThanOrEqual(0.5);
      expect(Math.abs(geometry.svgCenter.y - frameCenter.y)).toBeLessThanOrEqual(0.5);
      expect(Math.abs(geometry.contentCenter.x - frameCenter.x)).toBeLessThanOrEqual(0.5);
      expect(Math.abs(geometry.contentCenter.y - frameCenter.y)).toBeLessThanOrEqual(0.5);
      expect(geometry.documentWidth).toBeLessThanOrEqual(geometry.viewportWidth);
    });
  }
}

test("adjacent copy and confidence state do not move the ring", async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 900 });
  await page.emulateMedia({ reducedMotion: "reduce" });
  const boxes = [];

  for (const fixture of ["normal", "partial", "stale"]) {
    await page.goto(`/?lang=ru&fixture=${fixture}`);
    boxes.push((await ringGeometry(page)).frame);
  }

  for (const box of boxes.slice(1)) {
    expect(Math.abs(box.x - boxes[0].x)).toBeLessThanOrEqual(0.5);
    expect(Math.abs(box.y - boxes[0].y)).toBeLessThanOrEqual(0.5);
    expect(Math.abs(box.width - boxes[0].width)).toBeLessThanOrEqual(0.5);
    expect(Math.abs(box.height - boxes[0].height)).toBeLessThanOrEqual(0.5);
  }
});

test("the hero ring has no decorative outer shell", async ({ page }) => {
  await page.setViewportSize({ width: 430, height: 932 });
  await page.goto("/?lang=ru&fixture=normal");

  const styles = await page.locator("[data-readiness-ring] [data-gauge-frame]").evaluate((frame) => {
    const computed = getComputedStyle(frame);
    return {
      backgroundColor: computed.backgroundColor,
      borderWidth: computed.borderWidth,
      boxShadow: computed.boxShadow,
    };
  });

  expect(styles).toEqual({
    backgroundColor: "rgba(0, 0, 0, 0)",
    borderWidth: "0px",
    boxShadow: "none",
  });
});

test("the hero ring preserves its forced-colors boundary", async ({ page }) => {
  await page.emulateMedia({ forcedColors: "active" });
  await page.goto("/?lang=en&fixture=normal");

  const borderWidth = await page
    .locator("[data-readiness-ring] [data-gauge-frame]")
    .evaluate((frame) => getComputedStyle(frame).borderWidth);

  expect(borderWidth).toBe("1px");
});

for (const fixture of ["loading", "unavailable", "error"] as const) {
  test(`${fixture} state never fabricates a score`, async ({ page }) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.goto(`/?lang=en&fixture=${fixture}`);
    await expect(page.locator("[data-readiness-ring]")).toHaveCount(0);
    await expect(page.locator(`[data-resource-state="${fixture}"]`)).toBeVisible();
  });
}
