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

test("the theme switcher updates and persists the preference without navigation", async ({
  page,
}) => {
  await page.emulateMedia({ colorScheme: "light", reducedMotion: "reduce" });
  await page.goto("/?lang=en&fixture=normal");

  await page.getByRole("radio", { name: "Use dark theme" }).click();
  await expect(page.locator("html")).toHaveAttribute("dark-mode", "");
  expect(await page.evaluate(() => localStorage.getItem("theme"))).toBe("dark");
  expect(await page.evaluate(() => performance.getEntriesByType("navigation").length)).toBe(1);

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
