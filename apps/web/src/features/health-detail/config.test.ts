import { describe, expect, it } from "vitest";

import { healthSectionConfigs, resolveHealthSection } from "./config";

describe("health detail route config", () => {
  it.each(["activity", "cardio", "recovery"] as const)(
    "resolves /%s to the shared page configuration",
    (key) => {
      expect(resolveHealthSection(`/${key}`)).toBe(healthSectionConfigs[key]);
    },
  );

  it("does not claim legacy or metric routes", () => {
    expect(resolveHealthSection("/metrics")).toBeUndefined();
    expect(resolveHealthSection("/settings")).toBeUndefined();
  });
});
