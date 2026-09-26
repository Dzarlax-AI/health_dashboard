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
}

export function InsightPair({ locale, title, observation, meaning, action, ai, state }: InsightPairProps) {
  return (
    <div className="insight-pair" data-ai-state={state || (ai ? "ready" : "disabled")}>
      <article className="insight-pair__card insight-pair__card--server">
        <span className="insight-pair__label">Server Insight</span>
        {title ? <h1>{title}</h1> : null}
        <p>{observation}</p>
        {meaning && meaning !== observation ? <p>{meaning}</p> : null}
        {action ? <p className="insight-pair__action">{action}</p> : null}
      </article>
      {ai ? (
        <article className="insight-pair__card insight-pair__card--ai" data-stance={ai.stance}>
          <span className="insight-pair__label">AI Insight</span>
          <p>{ai.text}</p>
          {ai.alternative_action ? (
            <p className="insight-pair__action">
              <span>{translate(locale, "aiInsightAlternative")}</span> {ai.alternative_action}
            </p>
          ) : null}
        </article>
      ) : null}
    </div>
  );
}
