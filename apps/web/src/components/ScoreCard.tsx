import type { ReactNode } from "react";

import { ReadinessRing, type ScoreTone } from "./ReadinessRing";

interface ScoreCardProps {
  label: string;
  value: number;
  status: string;
  tone: Exclude<ScoreTone, "neutral">;
  children?: ReactNode;
}

export function ScoreCard({ label, value, status, tone, children }: ScoreCardProps) {
  return (
    <article className="score-card">
      <ReadinessRing
        value={value}
        label={label}
        status={status}
        tone={tone}
        size="card"
      />
      {children ? <div className="score-card__detail">{children}</div> : null}
    </article>
  );
}
