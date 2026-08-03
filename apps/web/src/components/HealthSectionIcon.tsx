import {
  Activity,
  BatteryMedium,
  CircleQuestionMark,
  HeartPulse,
  Moon,
  type LucideIcon,
} from "lucide-react";

const sectionIcons: Record<string, LucideIcon> = {
  activity: Activity,
  cardio: HeartPulse,
  recovery: BatteryMedium,
  sleep: Moon,
};

interface HealthSectionIconProps {
  sectionKey: string;
}

export function HealthSectionIcon({ sectionKey }: HealthSectionIconProps) {
  const Icon = sectionIcons[sectionKey] ?? CircleQuestionMark;

  return (
    <span className="section-link__icon" aria-hidden="true">
      <Icon size={22} strokeWidth={1.8} />
    </span>
  );
}
