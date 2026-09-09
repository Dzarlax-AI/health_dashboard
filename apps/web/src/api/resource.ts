export type ResourceState<T> =
  | { status: "loading" }
  | { status: "ready"; data: T; fetchedAt: string }
  | { status: "partial"; data: T; missing: readonly string[]; fetchedAt: string }
  | { status: "stale"; data: T; lastUpdated: string }
  | { status: "unavailable"; reason?: string }
  | { status: "unauthenticated"; loginPath: string }
  | { status: "error"; message: string; retryable: boolean };

export function hasUsableData<T>(
  state: ResourceState<T>,
): state is Extract<ResourceState<T>, { status: "ready" | "partial" | "stale" }> {
  return ["ready", "partial", "stale"].includes(state.status);
}
