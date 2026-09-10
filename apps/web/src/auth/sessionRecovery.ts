export const sessionRecoveryStorageKey = "health.auth.recovery.attempted";

export function sessionRecoveryURL(
  location: Pick<Location, "pathname" | "search" | "hash">,
): string {
  const next = `${location.pathname}${location.search}${location.hash}`;
  return `/auth/session?next=${encodeURIComponent(next)}`;
}

export function requestSessionRecovery(
  storage: Pick<Storage, "getItem" | "setItem" | "removeItem">,
  url: string,
  navigate: (target: string) => void,
): boolean {
  try {
    if (storage.getItem(sessionRecoveryStorageKey) === "1") {
      return false;
    }
    storage.setItem(sessionRecoveryStorageKey, "1");
    navigate(url);
    return true;
  } catch {
    try {
      storage.removeItem(sessionRecoveryStorageKey);
    } catch {
      // Storage can be unavailable in hardened browser contexts.
    }
    return false;
  }
}

export function clearSessionRecoveryAttempt(
  storage: Pick<Storage, "removeItem">,
): void {
  try {
    storage.removeItem(sessionRecoveryStorageKey);
  } catch {
    // A successful API response remains usable even if browser storage is unavailable.
  }
}
