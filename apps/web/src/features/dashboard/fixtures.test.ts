import { describe, expect, it } from "vitest";

import { resolveFixture } from "./fixtures";

describe("resolveFixture", () => {
  it("uses fixture data only for an explicit supported query value", () => {
    expect(resolveFixture("normal")).toBe("normal");
    expect(resolveFixture("partial")).toBe("partial");
    expect(resolveFixture(null)).toBeUndefined();
    expect(resolveFixture("")).toBeUndefined();
    expect(resolveFixture("unknown")).toBeUndefined();
  });
});
