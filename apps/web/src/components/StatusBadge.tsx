interface StatusBadgeProps {
  children: string;
  tone?: "good" | "warn" | "danger" | "neutral";
}

export function StatusBadge({ children, tone = "neutral" }: StatusBadgeProps) {
  return <span className={`status-badge status-badge--${tone}`}>{children}</span>;
}
