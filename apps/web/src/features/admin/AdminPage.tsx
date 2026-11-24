import { useCallback, useEffect, useState } from "react";

import { ClientApiError, getSession, type SessionResponse } from "../../api/client";
import { listUsers, type AdminUser } from "../../api/operations";
import {
  clearSessionRecoveryAttempt,
  recoverSessionOnUnauthorized,
} from "../../auth/sessionRecovery";
import { AppHeader } from "../../components/AppHeader";
import { StatusPanel } from "../../components/StatusPanel";
import { resolveLocale, translate } from "../../i18n";
import { tp } from "../../i18n/pages";
import { AdminDiagnosticsTab } from "./AdminDiagnosticsTab";
import { AdminEnergyTab } from "./AdminEnergyTab";
import { AdminAISettingsPanel, AdminUsersPanel } from "./AdminGlobalTabs";
import { AdminReadinessTab } from "./AdminReadinessTab";

type PageStatus =
  | { status: "loading" }
  | { status: "ready"; session: SessionResponse }
  | { status: "unauthenticated" }
  | { status: "forbidden" }
  | { status: "error"; message: string };

type ScopeTab = "users" | "general" | "profile";
type ProfileSubTab = "diagnostics" | "readiness" | "energy";

export function AdminPage() {
  const params = new URLSearchParams(window.location.search);
  const locale = resolveLocale(params.get("lang"));

  const [pageStatus, setPageStatus] = useState<PageStatus>({ status: "loading" });
  const [reloadKey, setReloadKey] = useState(0);
  const [users, setUsers] = useState<AdminUser[]>([]);
  const [scopeTab, setScopeTab] = useState<ScopeTab>("profile");
  const [activeSchema, setActiveSchema] = useState<string>("");
  const [activeUsername, setActiveUsername] = useState<string>("");
  const [profileSubTab, setProfileSubTab] = useState<ProfileSubTab>("diagnostics");

  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  const reloadUsers = useCallback(async () => {
    try {
      const res = await listUsers();
      setUsers(res.users ?? []);
    } catch {
      // single-tenant installations may have no registry users
    }
  }, []);

  useEffect(() => {
    const controller = new AbortController();

    getSession(controller.signal)
      .then(async (session) => {
        if (controller.signal.aborted) return;
        clearSessionRecoveryAttempt(window.sessionStorage);
        if (!session.is_admin) {
          setPageStatus({ status: "forbidden" });
          return;
        }
        setPageStatus({ status: "ready", session });
        try {
          const res = await listUsers(controller.signal);
          if (controller.signal.aborted) return;
          const fetchedUsers = res.users ?? [];
          setUsers(fetchedUsers);
          if (fetchedUsers.length > 0) {
            setScopeTab("users");
            const first = fetchedUsers[0];
            setActiveSchema(first.schema_name);
            setActiveUsername(first.username);
          }
        } catch (err: unknown) {
          if (err instanceof ClientApiError && err.status === 403) {
            setPageStatus({ status: "forbidden" });
          }
        }
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        if (
          recoverSessionOnUnauthorized(
            error,
            window.location,
            window.sessionStorage,
            (target) => window.location.assign(target),
          )
        ) {
          return;
        }
        if (error instanceof ClientApiError && error.status === 401) {
          setPageStatus({ status: "unauthenticated" });
          return;
        }
        if (error instanceof ClientApiError && error.status === 403) {
          setPageStatus({ status: "forbidden" });
          return;
        }
        setPageStatus({
          status: "error",
          message: error instanceof Error ? error.message : tp(locale, "errorGeneric"),
        });
      });

    return () => controller.abort();
  }, [locale, reloadKey]);

  const selectProfile = (schema: string, username: string) => {
    setActiveSchema(schema);
    setActiveUsername(username);
    setScopeTab("profile");
  };

  return (
    <div className="app-shell" data-locale={locale}>
      <AppHeader
        locale={locale}
        isAdmin={pageStatus.status === "ready" && pageStatus.session.is_admin}
      />
      <main className="subpage">
        <header className="subpage__header">
          <div className="subpage__heading">
            <a className="subpage__back" href={`/?lang=${locale}`}>
              ← {tp(locale, "backToToday")}
            </a>
            <h1>{tp(locale, "adminTitle")}</h1>
          </div>
        </header>

        {pageStatus.status === "loading" ? (
          <StatusPanel
            state="loading"
            title={tp(locale, "loading")}
            detail={translate(locale, "loadingDetail")}
          />
        ) : pageStatus.status === "unauthenticated" ? (
          <div className="standalone-status">
            <StatusPanel
              state="error"
              title={translate(locale, "signInTitle")}
              detail={translate(locale, "signInDetail")}
            />
            <a
              className="primary-action"
              href={`/login?next=${encodeURIComponent(`/admin?lang=${locale}`)}`}
            >
              {translate(locale, "signIn")}
            </a>
          </div>
        ) : pageStatus.status === "forbidden" ? (
          <StatusPanel
            state="error"
            title={tp(locale, "forbiddenTitle")}
            detail={tp(locale, "forbiddenDetail")}
          />
        ) : pageStatus.status === "error" ? (
          <div className="standalone-status">
            <StatusPanel
              state="error"
              title={tp(locale, "adminTitle")}
              detail={pageStatus.message}
            />
            <button
              type="button"
              className="primary-action"
              onClick={() => {
                setPageStatus({ status: "loading" });
                setReloadKey((n) => n + 1);
              }}
            >
              {tp(locale, "retry")}
            </button>
          </div>
        ) : (
          <>
            <nav className="admin-scope-bar" aria-label={tp(locale, "adminTitle")}>
              <div className="admin-scope-group" role="tablist">
                <span className="admin-scope-label">
                  {tp(locale, "adminScopeGlobal")}
                </span>
                {users.length > 0 ? (
                  <button
                    type="button"
                    role="tab"
                    aria-selected={scopeTab === "users"}
                    className={`btn btn--small${scopeTab === "users" ? " is-active" : ""}`}
                    onClick={() => setScopeTab("users")}
                  >
                    {tp(locale, "adminTabUsers")}
                  </button>
                ) : null}
                <button
                  type="button"
                  role="tab"
                  aria-selected={scopeTab === "general"}
                  className={`btn btn--small${scopeTab === "general" ? " is-active" : ""}`}
                  onClick={() => setScopeTab("general")}
                >
                  {tp(locale, "adminTabGeneral")}
                </button>
              </div>

              <div className="admin-scope-group" role="tablist">
                <span className="admin-scope-label">
                  {tp(locale, "adminScopeProfiles")}
                </span>
                {users.length > 0 ? (
                  users.map((user) => (
                    <button
                      key={user.schema_name}
                      type="button"
                      role="tab"
                      aria-selected={
                        scopeTab === "profile" && activeSchema === user.schema_name
                      }
                      className={`btn btn--small${
                        scopeTab === "profile" && activeSchema === user.schema_name
                          ? " is-active"
                          : ""
                      }`}
                      onClick={() => selectProfile(user.schema_name, user.username)}
                    >
                      {user.username}
                    </button>
                  ))
                ) : (
                  <button
                    type="button"
                    role="tab"
                    aria-selected={scopeTab === "profile"}
                    className={`btn btn--small${scopeTab === "profile" ? " is-active" : ""}`}
                    onClick={() => selectProfile("", tp(locale, "adminCurrentProfile"))}
                  >
                    {tp(locale, "adminCurrentProfile")}
                  </button>
                )}
              </div>
            </nav>

            {scopeTab === "users" ? (
              <AdminUsersPanel
                locale={locale}
                users={users}
                onReloadUsers={reloadUsers}
              />
            ) : scopeTab === "general" ? (
              <AdminAISettingsPanel locale={locale} />
            ) : (
              <>
                <div className="panel__header">
                  <div className="btn-row">
                    <strong>{tp(locale, "adminActiveScope")}:</strong>
                    <span>{activeUsername || tp(locale, "adminCurrentProfile")}</span>
                    {activeSchema ? <span className="pill">{activeSchema}</span> : null}
                  </div>
                  <div className="tab-strip" role="tablist">
                    <button
                      type="button"
                      role="tab"
                      aria-selected={profileSubTab === "diagnostics"}
                      className={`btn btn--small${
                        profileSubTab === "diagnostics" ? " is-active" : ""
                      }`}
                      onClick={() => setProfileSubTab("diagnostics")}
                    >
                      {tp(locale, "adminProfileDiagnostics")}
                    </button>
                    <button
                      type="button"
                      role="tab"
                      aria-selected={profileSubTab === "readiness"}
                      className={`btn btn--small${
                        profileSubTab === "readiness" ? " is-active" : ""
                      }`}
                      onClick={() => setProfileSubTab("readiness")}
                    >
                      {tp(locale, "adminProfileReadiness")}
                    </button>
                    <button
                      type="button"
                      role="tab"
                      aria-selected={profileSubTab === "energy"}
                      className={`btn btn--small${
                        profileSubTab === "energy" ? " is-active" : ""
                      }`}
                      onClick={() => setProfileSubTab("energy")}
                    >
                      {tp(locale, "adminProfileEnergy")}
                    </button>
                  </div>
                </div>

                {profileSubTab === "diagnostics" ? (
                  <AdminDiagnosticsTab
                    key={`diag-${activeSchema}`}
                    locale={locale}
                    schema={activeSchema || undefined}
                  />
                ) : profileSubTab === "readiness" ? (
                  <AdminReadinessTab
                    key={`readiness-${activeSchema}`}
                    locale={locale}
                    schema={activeSchema || undefined}
                  />
                ) : (
                  <AdminEnergyTab
                    key={`energy-${activeSchema}`}
                    locale={locale}
                    schema={activeSchema || undefined}
                  />
                )}
              </>
            )}
          </>
        )}
      </main>
    </div>
  );
}
