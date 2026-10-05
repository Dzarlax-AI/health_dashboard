import type { TodayInsightsResponse } from "../api/client";
import { translate, type Locale } from "../i18n";

type AIInsight = TodayInsightsResponse["ai_insight"];

interface InsightPairProps {
  locale: Locale;
  title?: string;
  observation: string;
  meaning?: string;
  action?: string;
  ai?: AIInsight;
  state?: string;
  aiUnavailableReason?: string;
  preview?: boolean;
}

export function InsightPair({ locale, title, observation, meaning, action, ai, state, aiUnavailableReason, preview }: InsightPairProps) {
  return (
    <div className="insight-pair" data-ai-state={state || (ai ? "ready" : "disabled")}>
      {preview ? <span className="insight-pair__preview">{translate(locale, "todayInsightsPreview")}</span> : null}
      <article className="insight-pair__card insight-pair__card--server">
        <span className="insight-pair__label">{translate(locale, "serverInsight")}</span>
        {title ? <h1>{title}</h1> : null}
        <p>{observation}</p>
        {meaning && meaning !== observation ? <p>{meaning}</p> : null}
        {action ? <p className="insight-pair__action">{action}</p> : null}
      </article>
      {ai ? (
        <article className="insight-pair__card insight-pair__card--ai" data-stance={ai.stance}>
          <span className="insight-pair__label">{translate(locale, "aiInsight")}</span>
          {ai.stale ? (
            <p className="insight-pair__previous" role="status">
              {translate(locale, "aiInsightPrevious")} {ai.source_date}
              {ai.generated_at ? ` · ${new Intl.DateTimeFormat(locale, { hour: "2-digit", minute: "2-digit" }).format(new Date(ai.generated_at))}` : ""}
              {` · ${translate(locale, state === "failed" ? "aiInsightRefreshFailed" : state === "cold" || state === "generating" ? "updating" : "aiInsightPreviousContext")}`}
            </p>
          ) : null}
          <p>{ai.text}</p>
          {ai.alternative_action ? (
            <p className="insight-pair__action">
              <span>{translate(locale, "aiInsightAlternative")}</span> {ai.alternative_action}
            </p>
          ) : null}
        </article>
      ) : aiUnavailableReason ? <p className="insight-pair__unavailable">{aiUnavailableReason}</p> : null}
    </div>
  );
}
