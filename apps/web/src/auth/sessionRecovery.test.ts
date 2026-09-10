import { describe, expect, it, vi } from "vitest";

import {
  clearSessionRecoveryAttempt,
  requestSessionRecovery,
  sessionRecoveryURL,
  sessionRecoveryStorageKey,
} from "./sessionRecovery";

describe("session recovery", () => {
  it("routes a session refresh through the protected backend endpoint", () => {
    expect(
      sessionRecoveryURL({ pathname: "/sleep", search: "?lang=en", hash: "#summary" }),
    ).toBe("/auth/session?next=%2Fsleep%3Flang%3Den%23summary");
  });

  it("navigates once and resets after a successful authenticated load", () => {
    const values = new Map<string, string>();
    const storage = {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => values.set(key, value),
      removeItem: (key: string) => values.delete(key),
    };
    const navigate = vi.fn();

    expect(requestSessionRecovery(storage, "/?lang=en", navigate)).toBe(true);
    expect(navigate).toHaveBeenCalledWith("/?lang=en");
    expect(values.get(sessionRecoveryStorageKey)).toBe("1");
    expect(requestSessionRecovery(storage, "/?lang=en", navigate)).toBe(false);

    clearSessionRecoveryAttempt(storage);
    expect(values.has(sessionRecoveryStorageKey)).toBe(false);
  });

  it("does not leave a retry marker when navigation fails", () => {
    const values = new Map<string, string>();
    const storage = {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => values.set(key, value),
      removeItem: (key: string) => values.delete(key),
    };

    expect(
      requestSessionRecovery(storage, "/?lang=en", () => {
        throw new Error("navigation blocked");
      }),
    ).toBe(false);
    expect(values.has(sessionRecoveryStorageKey)).toBe(false);
  });
});
