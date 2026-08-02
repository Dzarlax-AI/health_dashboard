export type ScoreTone = "readiness" | "energy" | "sleep" | "neutral";
export type ScoreSize = "hero" | "card";

interface ReadinessRingProps {
  value: number;
  label: string;
  status?: string;
  subline?: string;
  tone?: ScoreTone;
  size?: ScoreSize;
}

function clampScore(value: number): number {
  if (!Number.isFinite(value)) {
    return 0;
  }
  return Math.min(100, Math.max(0, Math.round(value)));
}

export function ReadinessRing({
  value,
  label,
  status,
  subline,
  tone = "readiness",
  size = "hero",
}: ReadinessRingProps) {
  const score = clampScore(value);
  const accessibleLabel = [label, `${score}%`, status, subline].filter(Boolean).join(", ");

  return (
    <figure
      className={`score-gauge score-gauge--${tone} score-gauge--${size}`}
      data-score-gauge
      data-readiness-ring={tone === "readiness" ? true : undefined}
      data-score={score}
      aria-label={accessibleLabel}
    >
      <div className="score-gauge__frame" data-gauge-frame>
        <svg
          className="score-gauge__svg"
          viewBox="0 0 120 120"
          preserveAspectRatio="xMidYMid meet"
          aria-hidden="true"
          focusable="false"
          data-gauge-svg
        >
          <circle className="score-gauge__track" cx="60" cy="60" r="50" />
          <circle
            className="score-gauge__progress"
            cx="60"
            cy="60"
            r="50"
            pathLength="100"
            strokeDasharray={`${score} 100`}
            transform="rotate(-90 60 60)"
          />
        </svg>
        <div className="score-gauge__content" data-gauge-content aria-hidden="true">
          <span className="score-gauge__value">
            {score}
            <small>%</small>
          </span>
        </div>
      </div>
      <figcaption className="score-gauge__caption">
        <strong className="score-gauge__label">{label}</strong>
        {status ? <span className="score-gauge__status">{status}</span> : null}
        {subline ? <span className="score-gauge__subline">{subline}</span> : null}
      </figcaption>
    </figure>
  );
}
