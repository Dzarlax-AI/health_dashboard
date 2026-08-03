import {
  Activity,
  BatteryMedium,
  CircleHelp,
  HeartPulse,
  Moon,
  type LucideIcon,
} from "lucide-react";

const sectionIcons: Record<string, LucideIcon> = {
  activity: Activity,
  battery: BatteryMedium,
  heart: HeartPulse,
  moon: Moon,
};

interface HealthSectionIconProps {
  name: string;
}

export function HealthSectionIcon({ name }: HealthSectionIconProps) {
  const Icon = sectionIcons[name] ?? CircleHelp;

  return (
    <span className="section-link__icon" aria-hidden="true">
      <Icon size={22} strokeWidth={1.8} />
    </span>
  );
}
