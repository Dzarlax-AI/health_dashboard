import { expect, test } from "@playwright/test";

test("uses the dark palette when the system prefers dark mode", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "dark", reducedMotion: "reduce" });
  await page.goto("/?lang=en&fixture=normal");

  await expect(page.locator("html")).toHaveAttribute("dark-mode", "");
  await expect(page.locator("html")).toHaveCSS("color-scheme", "dark");
  await expect(page.locator("body")).toHaveCSS("background-color", "rgb(17, 22, 19)");
  await expect(page.locator(".today-hero__recommendation")).toHaveCSS(
    "background-color",
    "rgba(27, 34, 30, 0.88)",
  );
  await expect(page.locator(".today-hero")).toHaveCSS(
    "background-image",
    /night-meadow-hero/,
  );
});

test("a saved light preference overrides a dark system preference", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "dark", reducedMotion: "reduce" });
  await page.addInitScript(() => localStorage.setItem("theme", "light"));
  await page.goto("/?lang=en&fixture=normal");

  await expect(page.locator("html")).not.toHaveAttribute("dark-mode", "");
  await expect(page.locator("html")).toHaveCSS("color-scheme", "light");
});

test("a saved dark preference overrides a light system preference", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "light", reducedMotion: "reduce" });
  await page.addInitScript(() => localStorage.setItem("theme", "dark"));
  await page.goto("/?lang=en&fixture=normal");

  await expect(page.locator("html")).toHaveAttribute("dark-mode", "");
  await expect(page.locator("html")).toHaveCSS("color-scheme", "dark");
});

test("an unsupported saved preference falls back to the system preference", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "dark", reducedMotion: "reduce" });
  await page.addInitScript(() => localStorage.setItem("theme", "unsupported"));
  await page.goto("/?lang=en&fixture=normal");

  await expect(page.locator("html")).toHaveAttribute("dark-mode", "");
  await expect(page.locator("html")).toHaveCSS("color-scheme", "dark");
});

test("the theme switcher updates and persists the preference without navigation", async ({
  page,
}) => {
  await page.emulateMedia({ colorScheme: "light", reducedMotion: "reduce" });
  await page.goto("/?lang=en&fixture=normal");

  let mainFrameNavigations = 0;
  page.on("framenavigated", (frame) => {
    if (frame === page.mainFrame()) {
      mainFrameNavigations += 1;
    }
  });

  await page.getByRole("radio", { name: "Use dark theme" }).click();
  await expect(page.locator("html")).toHaveAttribute("dark-mode", "");
  expect(await page.evaluate(() => localStorage.getItem("theme"))).toBe("dark");
  expect(mainFrameNavigations).toBe(0);

  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("dark-mode", "");

  await page.getByRole("radio", { name: "Use system theme" }).click();
  await expect(page.locator("html")).not.toHaveAttribute("dark-mode", "");
  expect(await page.evaluate(() => localStorage.getItem("theme"))).toBeNull();
});

test("the theme switcher remains visible at mobile width", async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 720 });
  await page.goto("/?lang=ru&fixture=normal");

  const switcher = page.getByRole("radiogroup", { name: "Тема" });
  await expect(switcher).toBeVisible();
  await expect(switcher.getByRole("radio")).toHaveCount(3);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(320);
});

test("the mobile header contains every control at 430px", async ({ page }) => {
  await page.setViewportSize({ width: 430, height: 932 });
  await page.goto("/?lang=ru&fixture=normal");

  const header = page.locator(".app-header");
  await expect(header).toBeVisible();
  await expect(page.getByRole("navigation", { name: "Основная навигация" })).toBeVisible();
  await expect(page.getByRole("radiogroup", { name: "Тема" })).toBeVisible();
  await expect(page.getByRole("navigation", { name: "Язык" })).toBeVisible();

  const layout = await header.evaluate((element) => {
    const box = (selector: string) => {
      const node = element.querySelector<HTMLElement>(selector);
      if (!node) {
        throw new Error(`Missing header element: ${selector}`);
      }
      const rect = node.getBoundingClientRect();
      return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom };
    };

    const rect = element.getBoundingClientRect();
    return {
      header: { left: rect.left, right: rect.right },
      brand: box(".app-header__brand"),
      links: box(".app-header__links"),
      theme: box(".theme-switcher"),
      locale: box(".locale-switcher"),
    };
  });

  for (const control of [layout.brand, layout.links, layout.theme, layout.locale]) {
    expect(control.left).toBeGreaterThanOrEqual(layout.header.left);
    expect(control.right).toBeLessThanOrEqual(layout.header.right);
  }
  expect(layout.links.top).toBeGreaterThanOrEqual(
    Math.max(layout.brand.bottom, layout.theme.bottom, layout.locale.bottom),
  );
  expect(Math.abs(layout.links.left - layout.header.left)).toBeLessThanOrEqual(0.5);
  expect(Math.abs(layout.links.right - layout.header.right)).toBeLessThanOrEqual(0.5);
  const documentWidth = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
  }));
  expect(documentWidth.scrollWidth).toBeLessThanOrEqual(documentWidth.clientWidth);
});
