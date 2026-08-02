import { ReadinessRing } from "../../components/ReadinessRing";
import { StatusPanel } from "../../components/StatusPanel";
import { Surface } from "../../components/Surface";
import { StatusBadge } from "../../components/StatusBadge";
import { translate, type Locale } from "../../i18n";
import type { DashboardViewModel } from "./model";

interface DashboardHeroProps {
  locale: Locale;
  model: DashboardViewModel;
}

export function DashboardHero({ locale, model }: DashboardHeroProps) {
  const dateLabel = model.date
    ? new Intl.DateTimeFormat(locale, {
        day: "numeric",
        month: "long",
        weekday: "long",
      }).format(new Date(`${model.date}T12:00:00`))
    : translate(locale, "today");

  if (!model.readiness) {
    return (
      <Surface tone="glass" className="today-hero">
        <p className="today-hero__date">{dateLabel}</p>
        <div className="today-hero__layout">
          <StatusPanel
            state="unavailable"
            title={translate(locale, "unavailableTitle")}
            detail={model.detail || translate(locale, "unavailableDetail")}
          />
        </div>
      </Surface>
    );
  }

  const badgeTone = model.state === "ready" ? "good" : "warn";
  const stateLabel = {
    ready: translate(locale, "state_ready"),
    partial: translate(locale, "state_partial"),
    stale: translate(locale, "state_stale"),
    unavailable: translate(locale, "state_unavailable"),
  }[model.state];

  return (
    <Surface tone="glass" className="today-hero">
      <p className="today-hero__date">{dateLabel}</p>
      <div className="today-hero__layout">
        <ReadinessRing
          value={model.readiness.value ?? 0}
          label={translate(locale, "readiness")}
          status={model.readiness.label}
        />
        <div className="today-hero__recommendation" data-resource-state={model.state}>
          <StatusBadge tone={badgeTone}>
            {stateLabel}
          </StatusBadge>
          <h1>{model.title}</h1>
          <p>{model.detail}</p>
          {model.checkinAnswer ? (
            <p className="today-hero__checkin">
              {translate(locale, "checkin")}: {model.checkinAnswer}
            </p>
          ) : null}
        </div>
      </div>
    </Surface>
  );
}
