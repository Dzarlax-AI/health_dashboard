import { hasUsableData, type ResourceState } from "./resource";

describe("resource states", () => {
  it("keeps partial and stale payloads usable", () => {
    const partial: ResourceState<{ score: number }> = {
      status: "partial",
      data: { score: 53 },
      missing: ["hrv"],
      fetchedAt: "2026-08-02T10:00:00Z",
    };
    const stale: ResourceState<{ score: number }> = {
      status: "stale",
      data: { score: 65 },
      lastUpdated: "2026-08-01T10:00:00Z",
    };

    expect(hasUsableData(partial)).toBe(true);
    expect(hasUsableData(stale)).toBe(true);
    expect(hasUsableData({ status: "loading" })).toBe(false);
  });
});
