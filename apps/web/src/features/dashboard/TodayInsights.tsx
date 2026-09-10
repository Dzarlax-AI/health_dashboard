import type { TodayInsightsResponse } from "../../api/client";
import { ArrowRight } from "lucide-react";
import { Surface } from "../../components/Surface";
import { StatusBadge } from "../../components/StatusBadge";
import { translate, type Locale } from "../../i18n";

interface TodayInsightsProps {
  locale: Locale;
  todayInsights?: TodayInsightsResponse;
}

type TodayInsightDomain = NonNullable<TodayInsightsResponse["domains"]>[number];
type TodayInsightDestination = TodayInsightDomain["destination"];

function destinationHref(
  destination: TodayInsightDestination,
  locale: Locale,
): string | undefined {
  if (destination.kind === "sleep" && destination.id === "sleep") {
    return `/sleep?lang=${locale}`;
  }
  if (destination.kind !== "section" || !destination.id) {
    return undefined;
  }
  return `/${encodeURIComponent(destination.id)}?lang=${locale}`;
}

function generationLabel(locale: Locale, state: TodayInsightsResponse["generation"]["state"]): string {
  switch (state) {
    case "cold":
    case "generating":
      return translate(locale, "todayInsightsUpdating");
    case "failed":
      return translate(locale, "todayInsightsFallback");
    case "disabled":
      return translate(locale, "todayInsightsFactual");
    default:
      return translate(locale, "todayInsightsFactual");
  }
}

function dataStateLabel(locale: Locale, state: TodayInsightDomain["data_state"]): string {
  switch (state) {
    case "fresh":
      return translate(locale, "state_ready");
    case "partial":
      return translate(locale, "state_partial");
    case "stale":
      return translate(locale, "state_stale");
    default:
      return translate(locale, "state_unavailable");
  }
}

function supplementaryObservation(domain: TodayInsightDomain): string | undefined {
  const summary = domain.summary.trim();
  const observation = domain.insight.observation.trim();
  if (!observation || observation === summary) {
    return undefined;
  }
  return observation;
}

export function TodayInsights({ locale, todayInsights }: TodayInsightsProps) {
  if (!todayInsights) {
    return null;
  }
  const { domains, changes, generation } = todayInsights;

  return (
    <section className="today-insights" aria-label={translate(locale, "todayFocus")}>
      <div className="today-insights__status">
        <StatusBadge tone={generation.state === "failed" ? "warn" : "neutral"}>
          {generationLabel(locale, generation.state)}
        </StatusBadge>
      </div>

      {(domains ?? []).length > 0 ? (
        <div className="today-insights__domains">
          {(domains ?? []).map((domain) => {
            const href = destinationHref(domain.destination, locale);
            const observation = supplementaryObservation(domain);
            const content = (
              <>
                <div className="today-insights__domain-meta">
                  <span>{domain.insight.title}</span>
                  <span>{dataStateLabel(locale, domain.data_state)}</span>
                </div>
                <strong>{domain.summary}</strong>
                {observation ? <p>{observation}</p> : null}
                {href ? <ArrowRight aria-hidden="true" size={18} strokeWidth={1.8} /> : null}
              </>
            );
            return href ? (
              <a className="today-insights__domain" href={href} key={domain.key}>
                {content}
              </a>
            ) : (
              <article className="today-insights__domain" key={domain.key}>
                {content}
              </article>
            );
          })}
        </div>
      ) : null}

      {(changes ?? []).length > 0 ? (
        <Surface className="today-insights__changes">
          <div className="section-heading">
            <div>
              <p>{translate(locale, "todayChanges")}</p>
              <h2>{translate(locale, "todayChangesTitle")}</h2>
            </div>
          </div>
          <ul className="plain-list">
            {(changes ?? []).map((change) => (
              <li key={change.id}>
                <span>
                  <strong>{change.title}</strong>
                  <span>{change.detail}</span>
                </span>
              </li>
            ))}
          </ul>
        </Surface>
      ) : null}
    </section>
  );
}
