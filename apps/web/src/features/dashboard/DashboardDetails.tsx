import type {
  AIBriefingResponse,
  EnergyHistoryResponse,
  ReadinessHistoryResponse,
} from "../../api/client";
import { Surface } from "../../components/Surface";
import { StatusBadge } from "../../components/StatusBadge";
import { LazyTrendChart } from "../../components/charts/LazyTrendChart";
import type { TrendPoint } from "../../components/charts/TrendChart";
import { translate, type Locale } from "../../i18n";
import type { DashboardViewModel } from "./model";

interface DashboardDetailsProps {
  locale: Locale;
  model: DashboardViewModel;
  ai?: AIBriefingResponse;
  readinessHistory?: ReadinessHistoryResponse;
  energyHistory?: EnergyHistoryResponse;
}

function dayLabel(date: string, locale: Locale): string {
  const parsed = new Date(`${date}T12:00:00`);
  return Number.isNaN(parsed.valueOf())
    ? date
    : new Intl.DateTimeFormat(locale, { day: "numeric", month: "short" }).format(parsed);
}

export function DashboardDetails({
  locale,
  model,
  ai,
  readinessHistory,
  energyHistory,
}: DashboardDetailsProps) {
  const readinessPoints: TrendPoint[] = (readinessHistory?.points ?? []).map((point) => ({
    label: dayLabel(point.date, locale),
    value: point.score,
  }));
  const energyPoints: TrendPoint[] =
    energyHistory?.granularity === "day"
      ? (energyHistory.points ?? []).map((point) => ({
          label: dayLabel(point.date, locale),
          value: point.current_eod,
        }))
      : [];
  const aiSections = ai?.sections ?? [];

  return (
    <>
      {model.alerts && model.alerts.length > 0 ? (
        <Surface className="content-card alert-card">
          <div className="section-heading">
            <div>
              <p>{translate(locale, "attention")}</p>
              <h2>{translate(locale, "alerts")}</h2>
            </div>
          </div>
          <ul className="plain-list">
            {model.alerts.map((alert) => (
              <li key={`${alert.metric}-${alert.text}`}>
                <StatusBadge tone={alert.severity === "high" ? "danger" : "warn"}>
                  {alert.metric}
                </StatusBadge>
                <span>{alert.text}</span>
              </li>
            ))}
          </ul>
        </Surface>
      ) : null}

      {aiSections.length > 0 || ai?.insight ? (
        <Surface className="content-card insight-card">
          <div className="section-heading">
            <div>
              <p>{translate(locale, "personalInsight")}</p>
              <h2>{translate(locale, "whatMatters")}</h2>
            </div>
            {ai?.generating ? (
              <StatusBadge>{translate(locale, "updating")}</StatusBadge>
            ) : null}
          </div>
          {aiSections.length > 0
            ? aiSections.map((section) => (
                <article key={section.key}>
                  <h3>{section.header}</h3>
                  <p>{section.body}</p>
                </article>
              ))
            : <p>{ai?.insight}</p>}
        </Surface>
      ) : null}

      <section className="trend-grid" aria-label={translate(locale, "trends")}>
        <Surface as="article" className="content-card trend-card">
          <div className="section-heading">
            <h2>{translate(locale, "readinessTrend")}</h2>
            <a href={`/?lang=${locale}`}>{translate(locale, "today")}</a>
          </div>
          <LazyTrendChart
            ariaLabel={translate(locale, "readinessTrend")}
            data={readinessPoints}
            tone="readiness"
          />
          {readinessPoints.length < 2 ? (
            <p className="empty-note">{translate(locale, "historyAccruing")}</p>
          ) : null}
        </Surface>
        <Surface as="article" className="content-card trend-card">
          <div className="section-heading">
            <h2>{translate(locale, "energyTrend")}</h2>
          </div>
          <LazyTrendChart
            ariaLabel={translate(locale, "energyTrend")}
            data={energyPoints}
            tone="energy"
          />
          {energyPoints.length < 2 ? (
            <p className="empty-note">{translate(locale, "historyAccruing")}</p>
          ) : null}
        </Surface>
      </section>

      {model.sections && model.sections.length > 0 ? (
        <section className="section-grid" aria-label={translate(locale, "healthSections")}>
          {model.sections.map((section) => (
            <a
              className="section-link"
              href={`/${section.key}?lang=${locale}`}
              key={section.key}
            >
              <span aria-hidden="true">{section.icon}</span>
              <div>
                <strong>{section.title}</strong>
                <p>{section.summary}</p>
              </div>
              <span aria-hidden="true">→</span>
            </a>
          ))}
        </section>
      ) : null}

      {model.metricCards && model.metricCards.length > 0 ? (
        <section className="metric-grid" aria-label={translate(locale, "metrics")}>
          {model.metricCards.slice(0, 6).map((metric) => (
            <a
              className="metric-card"
              href={`/metrics/${encodeURIComponent(metric.metric)}?lang=${locale}`}
              key={metric.metric}
            >
              <span>{metric.name}</span>
              <strong>
                {metric.value} <small>{metric.unit}</small>
              </strong>
              <em>{metric.trend_label}</em>
            </a>
          ))}
        </section>
      ) : null}
    </>
  );
}
