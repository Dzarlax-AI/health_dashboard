import { fireEvent, render, screen, waitFor } from "@testing-library/react";
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
});
