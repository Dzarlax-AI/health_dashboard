import { useState } from "react";

import {
  answerInboxPrompt,
  ClientApiError,
  type PendingCheckinPrompt,
  type PromptAnswerRequest,
  type PromptAnswerResponse,
} from "../../api/client";
import { translate, type Locale } from "../../i18n";

interface PendingCheckinCardProps {
  locale: Locale;
  prompts: PendingCheckinPrompt[];
  onAnswered?: (promptId: string, response: PromptAnswerResponse) => void;
  onExpired?: (promptId: string) => void;
}

function eyebrowForPromptKind(
  locale: Locale,
  kind?: PendingCheckinPrompt["kind"],
): string {
  switch (kind) {
    case "prompt_wake_feedback":
      return translate(locale, "inboxKindWakeFeedback");
    case "prompt_context":
      return translate(locale, "inboxKindContext");
    default:
      return translate(locale, "checkin");
  }
}

type CardSubmissionState =
  | { kind: "idle" }
  | { kind: "submitting"; promptId: string; optionId: string }
  | { kind: "error"; promptId: string; message: string }
  | {
      kind: "resolved";
      promptId: string;
      status: "saved" | "already_answered" | "expired";
      answerLabel?: string;
    };

export function PendingCheckinCard({
  locale,
  prompts,
  onAnswered,
  onExpired,
}: PendingCheckinCardProps) {
  const [submission, setSubmission] = useState<CardSubmissionState>({ kind: "idle" });

  const activePrompt = prompts[0];
  if (!activePrompt && submission.kind !== "resolved") {
    return null;
  }

  if (submission.kind === "resolved" && (!activePrompt || activePrompt.prompt_id === submission.promptId)) {
    const notice =
      submission.status === "expired"
        ? translate(locale, "inboxPromptExpired")
        : submission.status === "already_answered"
          ? `${translate(locale, "inboxPromptAlreadyAnswered")}${submission.answerLabel ? `: ${submission.answerLabel}` : ""}`
          : `${translate(locale, "inboxPromptSaved")}${submission.answerLabel ? `: ${submission.answerLabel}` : ""}`;
    return (
      <section className="inbox-prompt-card inbox-prompt-card--resolved" aria-live="polite">
        <p className="inbox-prompt-card__notice">{notice}</p>
      </section>
    );
  }

  if (!activePrompt) {
    return null;
  }

  const isSubmitting =
    submission.kind === "submitting" && submission.promptId === activePrompt.prompt_id;
  const errorMessage =
    submission.kind === "error" && submission.promptId === activePrompt.prompt_id
      ? submission.message
      : undefined;

  async function handleSelect(optionId: string) {
    if (!activePrompt || isSubmitting) {
      return;
    }
    setSubmission({
      kind: "submitting",
      promptId: activePrompt.prompt_id,
      optionId,
    });
    try {
      const response = await answerInboxPrompt(activePrompt.prompt_id, {
        answer: optionId as PromptAnswerRequest["answer"],
        channel: "web",
      });
      const canonicalOption = (activePrompt.options ?? []).find(
        (opt) => opt.id === response.answer,
      );
      const answerLabel = canonicalOption?.label ?? response.answer;
      setSubmission({
        kind: "resolved",
        promptId: activePrompt.prompt_id,
        status: response.status,
        answerLabel,
      });
      onAnswered?.(activePrompt.prompt_id, response);
    } catch (error: unknown) {
      if (error instanceof ClientApiError && error.status === 409) {
        setSubmission({
          kind: "resolved",
          promptId: activePrompt.prompt_id,
          status: "expired",
        });
        onExpired?.(activePrompt.prompt_id);
        return;
      }
      setSubmission({
        kind: "error",
        promptId: activePrompt.prompt_id,
        message: translate(locale, "inboxPromptSaveError"),
      });
    }
  }

  return (
    <section className="inbox-prompt-card" aria-label={activePrompt.title}>
      <div className="inbox-prompt-card__header">
        <p className="inbox-prompt-card__eyebrow">
          {eyebrowForPromptKind(locale, activePrompt.kind)}
        </p>
        <span className="inbox-prompt-card__date">{activePrompt.local_date}</span>
      </div>
      <h2 className="inbox-prompt-card__title">{activePrompt.title}</h2>
      <div className="inbox-prompt-options">
        {(activePrompt.options ?? []).map((option) => {
          const busy =
            submission.kind === "submitting" &&
            submission.promptId === activePrompt.prompt_id &&
            submission.optionId === option.id;
          return (
            <button
              key={option.id}
              type="button"
              className="inbox-prompt-option"
              disabled={isSubmitting}
              aria-busy={busy || undefined}
              onClick={() => void handleSelect(option.id)}
            >
              {option.label}
            </button>
          );
        })}
      </div>
      {errorMessage ? (
        <p className="inbox-prompt-card__error" role="alert">
          {errorMessage}
        </p>
      ) : null}
    </section>
  );
}
