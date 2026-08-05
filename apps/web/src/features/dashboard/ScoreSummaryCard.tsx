import { ReadinessRing } from "../../components/ReadinessRing";
import { Surface } from "../../components/Surface";
import type { DashboardScore } from "./model";

interface ScoreSummaryCardProps {
  score: DashboardScore;
  fallbackLabel: string;
  displayStatus?: string;
  href?: string;
}

export function ScoreSummaryCard({
  score,
  fallbackLabel,
  displayStatus,
  href,
}: ScoreSummaryCardProps) {
  const card = (
    <Surface as="article" className="summary-card">
      {score.value !== undefined ? (
        <ReadinessRing
          value={score.value}
          label={fallbackLabel}
          status={displayStatus || score.label || score.status}
          subline={score.detail}
          tone={score.tone}
          size="card"
        />
      ) : (
        <div className="summary-card__text">
          <p className="summary-card__eyebrow">{fallbackLabel}</p>
          <strong>{score.detail}</strong>
          <span>{score.status}</span>
        </div>
      )}
    </Surface>
  );
  return href ? <a className="summary-card-link" href={href}>{card}</a> : card;
}
