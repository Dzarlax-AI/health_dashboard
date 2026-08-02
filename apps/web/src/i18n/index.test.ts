import { resolveLocale, supportedLocales, translate } from "./index";

describe("localisation", () => {
  it("constrains locale input to the supported contract", () => {
    expect(supportedLocales).toEqual(["en", "ru", "sr"]);
    expect(resolveLocale("ru")).toBe("ru");
    expect(resolveLocale("schema=other_tenant")).toBe("en");
    expect(resolveLocale(null)).toBe("en");
  });

  it.each(supportedLocales)("has translated readiness copy for %s", (locale) => {
    expect(translate(locale, "readiness")).not.toBe("");
    expect(translate(locale, "normalSummary")).not.toBe("");
  });
});
