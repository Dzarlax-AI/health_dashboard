import { supportedLocales, translate, type Locale } from "../i18n";

interface AppHeaderProps {
  locale: Locale;
  isAdmin?: boolean;
}

function localizedHref(path: string, locale: Locale): string {
  const separator = path.includes("?") ? "&" : "?";
  return `${path}${separator}lang=${locale}`;
}

export function AppHeader({ locale, isAdmin = false }: AppHeaderProps) {
  const params = new URLSearchParams(window.location.search);

  return (
    <header className="app-header">
      <a className="app-header__brand" href={localizedHref("/", locale)}>
        {translate(locale, "appTitle")}
      </a>
      <nav className="app-header__links" aria-label={translate(locale, "primaryNav")}>
        <a href={localizedHref("/metrics", locale)}>
          {translate(locale, "metrics")}
        </a>
        <a href={localizedHref("/settings", locale)}>
          {translate(locale, "settings")}
        </a>
        {isAdmin ? (
          <a href={localizedHref("/admin", locale)}>{translate(locale, "admin")}</a>
        ) : null}
        <form method="post" action="/logout" className="logout-form">
          <button type="submit">{translate(locale, "logout")}</button>
        </form>
      </nav>
      <nav className="locale-switcher" aria-label={translate(locale, "languageNav")}>
        {supportedLocales.map((candidate) => {
          const candidateParams = new URLSearchParams(params);
          candidateParams.set("lang", candidate);
          return (
            <a
              key={candidate}
              href={`/?${candidateParams.toString()}`}
              aria-current={candidate === locale ? "page" : undefined}
            >
              {candidate.toUpperCase()}
            </a>
          );
        })}
      </nav>
    </header>
  );
}
