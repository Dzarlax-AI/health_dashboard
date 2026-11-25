import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { vi } from "vitest";

import { ClientApiError, type PendingCheckinPrompt } from "../../api/client";
import * as clientApi from "../../api/client";
import { PendingCheckinCard } from "./PendingCheckinCard";
import { InboxPage } from "./InboxPage";

const samplePrompt: PendingCheckinPrompt = {
  prompt_id: "11111111-1111-1111-1111-111111111111",
  local_date: "2026-11-25",
  expires_at: "2026-11-25T10:00:00Z",
  title: "How are you feeling today?",
  options: [
    { id: "great", label: "Great" },
    { id: "ok", label: "OK" },
    { id: "meh", label: "Meh" },
    { id: "sick", label: "Sick" },
  ],
};

describe("PendingCheckinCard", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("renders options in server order and confirms saved answer", async () => {
    const onAnswered = vi.fn();
    vi.spyOn(clientApi, "answerInboxPrompt").mockResolvedValue({
      status: "saved",
      answer: "great",
      answered_at: "2026-11-25T08:05:00Z",
      timing: "on_time",
    });

    render(
      <PendingCheckinCard
        locale="en"
        prompts={[samplePrompt]}
        onAnswered={onAnswered}
      />,
    );

    const buttons = screen.getAllByRole("button");
    expect(buttons.map((b) => b.textContent)).toEqual(["Great", "OK", "Meh", "Sick"]);

    fireEvent.click(buttons[0]);

    await waitFor(() => {
      expect(screen.getByText(/Saved: Great/)).toBeInTheDocument();
    });
    expect(clientApi.answerInboxPrompt).toHaveBeenCalledWith(samplePrompt.prompt_id, {
      answer: "great",
      channel: "web",
    });
    expect(onAnswered).toHaveBeenCalledTimes(1);
  });

  it("shows canonical saved answer on race mismatch (already_answered)", async () => {
    vi.spyOn(clientApi, "answerInboxPrompt").mockResolvedValue({
      status: "already_answered",
      answer: "sick",
      answered_at: "2026-11-25T08:01:00Z",
      timing: "on_time",
    });

    render(<PendingCheckinCard locale="en" prompts={[samplePrompt]} />);
    fireEvent.click(screen.getByRole("button", { name: "Great" }));

    await waitFor(() => {
      expect(screen.getByText(/Already answered: Sick/)).toBeInTheDocument();
    });
  });

  it("keeps buttons retryable on network failure without showing saved", async () => {
    vi.spyOn(clientApi, "answerInboxPrompt").mockRejectedValue(
      new ClientApiError(503, "Service unavailable"),
    );

    render(<PendingCheckinCard locale="en" prompts={[samplePrompt]} />);
    fireEvent.click(screen.getByRole("button", { name: "OK" }));

    await waitFor(() => {
      expect(screen.getByRole("alert")).toHaveTextContent(
        "Could not save your answer. Try again.",
      );
    });
    expect(screen.getByRole("button", { name: "OK" })).not.toBeDisabled();
    expect(screen.queryByText(/Saved/)).not.toBeInTheDocument();
  });

  it("handles 409 expired by notifying parent and stopping retries", async () => {
    const onExpired = vi.fn();
    vi.spyOn(clientApi, "answerInboxPrompt").mockRejectedValue(
      new ClientApiError(409, "Conflict"),
    );

    render(
      <PendingCheckinCard
        locale="en"
        prompts={[samplePrompt]}
        onExpired={onExpired}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Meh" }));

    await waitFor(() => {
      expect(
        screen.getByText("This check-in is no longer active for today."),
      ).toBeInTheDocument();
    });
    expect(onExpired).toHaveBeenCalledWith(samplePrompt.prompt_id);
  });
});

describe("InboxPage", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    window.history.replaceState({}, "", "/");
  });

  it("renders morning report detail as safe plain text", async () => {
    window.history.replaceState(
      {},
      "",
      "/inbox/22222222-2222-2222-2222-222222222222?lang=en",
    );
    vi.spyOn(clientApi, "getInboxItem").mockResolvedValue({
      id: "22222222-2222-2222-2222-222222222222",
      kind: "morning_report",
      local_date: "2026-11-25",
      created_at: "2026-11-25T08:00:00Z",
      title: "Morning Report",
      body: "Sleep: 7h 30m\nRecovery: Solid <script>alert(1)</script>",
    });
    vi.spyOn(clientApi, "getSession").mockResolvedValue({ is_admin: false });

    const { container } = render(
      <InboxPage itemId="22222222-2222-2222-2222-222222222222" />,
    );

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Morning Report" })).toBeInTheDocument();
    });
    expect(container.querySelector("script")).toBeNull();
    expect(screen.getByText(/Sleep: 7h 30m/)).toBeInTheDocument();
  });

  it("allows same-day late answer on expired check-in detail and localizes answers", async () => {
    window.history.replaceState(
      {},
      "",
      "/inbox/44444444-4444-4444-4444-444444444444?lang=ru",
    );
    const detailSpy = vi
      .spyOn(clientApi, "getInboxItem")
      .mockResolvedValueOnce({
        id: "44444444-4444-4444-4444-444444444444",
        kind: "prompt_checkin",
        local_date: "2026-11-25",
        created_at: "2026-11-25T08:00:00Z",
        title: "Как самочувствие?",
        prompt_id: "44444444-4444-4444-4444-444444444444",
        status: "expired",
        options: [
          { id: "great", label: "Отлично" },
          { id: "ok", label: "Нормально" },
          { id: "meh", label: "Не очень" },
          { id: "sick", label: "Болен(а)" },
        ],
      })
      .mockResolvedValueOnce({
        id: "44444444-4444-4444-4444-444444444444",
        kind: "prompt_checkin",
        local_date: "2026-11-25",
        created_at: "2026-11-25T08:00:00Z",
        title: "Как самочувствие?",
        prompt_id: "44444444-4444-4444-4444-444444444444",
        status: "late_answered",
        answer: "sick",
      });
    vi.spyOn(clientApi, "getSession").mockResolvedValue({ is_admin: false });
    const answerSpy = vi.spyOn(clientApi, "answerInboxPrompt").mockResolvedValue({
      status: "saved",
      answer: "sick",
      answered_at: "2026-11-25T13:00:00Z",
      timing: "late",
    });

    render(<InboxPage itemId="44444444-4444-4444-4444-444444444444" />);

    const sickBtn = await screen.findByRole("button", { name: "Болен(а)" });
    fireEvent.click(sickBtn);

    await waitFor(() => {
      expect(screen.getByText("Отвечено поздно: Болен(а)")).toBeInTheDocument();
    });
    expect(answerSpy).toHaveBeenCalledWith("44444444-4444-4444-4444-444444444444", {
      answer: "sick",
      channel: "web",
    });
    expect(detailSpy).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole("button", { name: "Болен(а)" })).not.toBeInTheDocument();
  });

  it("discards stale load-more responses when a feed reload intervenes", async () => {
    window.history.replaceState({}, "", "/inbox?lang=en");
    let resolveLoadMore!: (value: clientApi.InboxFeedResponse) => void;
    const loadMorePromise = new Promise<clientApi.InboxFeedResponse>((resolve) => {
      resolveLoadMore = resolve;
    });
    vi.spyOn(clientApi, "getInbox")
      .mockResolvedValueOnce({
        items: [
          {
            id: "a1",
            kind: "morning_report",
            local_date: "2026-11-25",
            created_at: "2026-11-25T08:00:00Z",
            title: "Morning Report",
            preview: "Page 1 initial",
          },
        ],
        next_cursor: "cursor-page-2",
      })
      .mockImplementationOnce(() => loadMorePromise)
      .mockResolvedValueOnce({
        items: [
          {
            id: "a2",
            kind: "morning_report",
            local_date: "2026-11-25",
            created_at: "2026-11-25T08:05:00Z",
            title: "Morning Report",
            preview: "Page 1 reloaded",
          },
        ],
        next_cursor: "cursor-page-2-fresh",
      });
    vi.spyOn(clientApi, "getPendingInboxPrompts")
      .mockResolvedValueOnce({ prompts: [samplePrompt] })
      .mockResolvedValueOnce({ prompts: [] });
    vi.spyOn(clientApi, "getSession").mockResolvedValue({ is_admin: false });
    vi.spyOn(clientApi, "answerInboxPrompt").mockResolvedValue({
      status: "saved",
      answer: "great",
      answered_at: "2026-11-25T08:10:00Z",
      timing: "on_time",
    });

    render(<InboxPage />);

    const loadMoreBtn = await screen.findByRole("button", { name: "Load older" });
    fireEvent.click(loadMoreBtn);

    // Answer pending prompt while page 2 is still in flight -> triggers feed reload.
    fireEvent.click(screen.getByRole("button", { name: "Great" }));

    await waitFor(() => {
      expect(screen.getByText("Page 1 reloaded")).toBeInTheDocument();
    });

    // Resolve the stale page 2 after reload completed.
    resolveLoadMore({
      items: [
        {
          id: "stale-page-2",
          kind: "morning_report",
          local_date: "2026-11-20",
          created_at: "2026-11-20T08:00:00Z",
          title: "Morning Report",
          preview: "Stale older item",
        },
      ],
      next_cursor: "cursor-stale-3",
    });

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Load older" })).not.toBeDisabled();
    });
    expect(screen.queryByText("Stale older item")).not.toBeInTheDocument();
  });

  it("waits for the refreshed cursor before requesting another page", async () => {
    window.history.replaceState({}, "", "/inbox?lang=en");
    const firstItem = {
      id: "a1",
      kind: "morning_report" as const,
      local_date: "2026-11-25",
      created_at: "2026-11-25T08:00:00Z",
      title: "Morning Report",
    };
    let completeReload!: (value: clientApi.InboxFeedResponse) => void;
    const pendingReload = new Promise<clientApi.InboxFeedResponse>((resolve) => {
      completeReload = resolve;
    });
    const oldPageAborted = vi.fn();
    const getInbox = vi.spyOn(clientApi, "getInbox")
      .mockResolvedValueOnce({ items: [firstItem], next_cursor: "old-cursor" })
      .mockImplementationOnce((_locale, _query, signal) => new Promise((_resolve, reject) => {
        signal?.addEventListener("abort", () => {
          oldPageAborted();
          reject(new DOMException("Aborted", "AbortError"));
        }, { once: true });
      }))
      .mockImplementationOnce(() => pendingReload)
      .mockResolvedValue({ items: [] });
    vi.spyOn(clientApi, "getPendingInboxPrompts")
      .mockResolvedValueOnce({ prompts: [samplePrompt] })
      .mockResolvedValue({ prompts: [] });
    vi.spyOn(clientApi, "getSession").mockResolvedValue({ is_admin: false });
    vi.spyOn(clientApi, "answerInboxPrompt").mockResolvedValue({
      status: "saved",
      answer: "great",
      answered_at: "2026-11-25T08:10:00Z",
      timing: "on_time",
    });

    render(<InboxPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Load older" }));
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Great" })));
    expect(oldPageAborted).toHaveBeenCalledOnce();
    expect(getInbox).toHaveBeenCalledTimes(3);

    const loadMore = screen.getByRole("button", { name: "Load older" });
    expect(loadMore).toBeDisabled();
    fireEvent.click(loadMore);
    expect(getInbox).toHaveBeenCalledTimes(3);

    await act(async () => {
      completeReload({ items: [firstItem], next_cursor: "fresh-cursor" });
    });
    expect(loadMore).not.toBeDisabled();
    fireEvent.click(loadMore);
    expect(getInbox).toHaveBeenLastCalledWith(
      "en", { limit: 20, cursor: "fresh-cursor" }, expect.any(AbortSignal),
    );
    await waitFor(() => expect(loadMore).not.toBeInTheDocument());
  });
});
