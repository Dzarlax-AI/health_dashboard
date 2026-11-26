import { supportedLocales, translate, type Locale } from "../i18n";
import { ThemeSwitcher } from "./ThemeSwitcher";

interface AppHeaderProps {
  locale: Locale;
  isAdmin?: boolean;
  showInbox?: boolean;
  inboxUnreadCount?: number;
}

function localizedHref(path: string, locale: Locale): string {
  const separator = path.includes("?") ? "&" : "?";
  return `${path}${separator}lang=${locale}`;
}

export function AppHeader({
  locale,
  isAdmin = false,
  showInbox = false,
  inboxUnreadCount = 0,
}: AppHeaderProps) {
  const params = new URLSearchParams(window.location.search);
  const currentPath = window.location.pathname;
  const unreadLabel = inboxUnreadCount > 99 ? "99+" : String(inboxUnreadCount);

  return (
    <header className="app-header">
      <a className="app-header__brand" href={localizedHref("/", locale)}>
        {translate(locale, "appTitle")}
      </a>
      <nav className="app-header__links" aria-label={translate(locale, "primaryNav")}>
        {showInbox ? (
          <a
            href={localizedHref("/inbox", locale)}
            className="app-header__inbox-link"
            aria-current={
              currentPath === "/inbox" || currentPath.startsWith("/inbox/")
                ? "page"
                : undefined
            }
          >
            <span>{translate(locale, "inbox")}</span>
            {inboxUnreadCount > 0 ? (
              <span
                className="nav-unread-badge"
                aria-label={`${inboxUnreadCount} ${translate(locale, "inboxUnread")}`}
              >
                {unreadLabel}
              </span>
            ) : null}
          </a>
        ) : null}
        <a
          href={localizedHref("/metrics", locale)}
          aria-current={
            currentPath === "/metrics" || currentPath.startsWith("/metrics/")
              ? "page"
              : undefined
          }
        >
          {translate(locale, "metrics")}
        </a>
        <a
          href={localizedHref("/settings", locale)}
          aria-current={currentPath === "/settings" ? "page" : undefined}
        >
          {translate(locale, "settings")}
        </a>
        {isAdmin ? (
          <a
            href={localizedHref("/admin", locale)}
            aria-current={currentPath === "/admin" ? "page" : undefined}
          >
            {translate(locale, "admin")}
          </a>
        ) : null}
        <form method="post" action="/logout" className="logout-form">
          <button type="submit">{translate(locale, "logout")}</button>
        </form>
      </nav>
      <ThemeSwitcher locale={locale} />
      <nav className="locale-switcher" aria-label={translate(locale, "languageNav")}>
        {supportedLocales.map((candidate) => {
          const candidateParams = new URLSearchParams(params);
          candidateParams.set("lang", candidate);
          return (
            <a
              key={candidate}
              href={`${currentPath}?${candidateParams.toString()}`}
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
