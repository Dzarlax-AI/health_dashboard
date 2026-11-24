import { useCallback, useEffect, useState, type FormEvent } from "react";

import {
  createUser,
  getAISettings,
  getUserAPIKey,
  listProviderModels,
  updateAISettings,
  type AdminAISettingsResponse,
  type AdminUser,
  type AIModelInfo,
} from "../../api/operations";
import type { Locale } from "../../i18n";
import { tp } from "../../i18n/pages";

export function AdminAISettingsPanel({ locale }: { locale: Locale }) {
  const [settings, setSettings] = useState<AdminAISettingsResponse | null>(null);
  const [providerId, setProviderId] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [clearApiKey, setClearApiKey] = useState(false);
  const [model, setModel] = useState("");
  const [reasoning, setReasoning] = useState("");
  const [maxTokens, setMaxTokens] = useState("5000");
  const [models, setModels] = useState<AIModelInfo[]>([]);
  const [loadingModels, setLoadingModels] = useState(false);
  const [saving, setSaving] = useState(false);
  const [notice, setNotice] = useState<{ tone: "good" | "danger"; text: string } | null>(null);

  const applyProvider = useCallback(
    (data: AdminAISettingsResponse, id: string) => {
      const target =
        data.providers.find((p) => p.id === id) ?? data.providers[0];
      if (!target) return;
      setProviderId(target.id);
      setApiKey("");
      setClearApiKey(false);
      setModel(target.model || target.default_model || "");
      setReasoning(target.reasoning_effort || target.reasoning_efforts[0] || "");
      setModels([]);
    },
    [],
  );

  const loadSettings = useCallback(async () => {
    try {
      const data = await getAISettings();
      setSettings(data);
      setMaxTokens(String(data.max_output_tokens || 5000));
      applyProvider(data, data.provider);
    } catch (error: unknown) {
      setNotice({
        tone: "danger",
        text: error instanceof Error ? error.message : tp(locale, "errorGeneric"),
      });
    }
  }, [applyProvider, locale]);

  useEffect(() => {
    const controller = new AbortController();
    getAISettings(controller.signal)
      .then((data) => {
        if (controller.signal.aborted) return;
        setSettings(data);
        setMaxTokens(String(data.max_output_tokens || 5000));
        applyProvider(data, data.provider);
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setNotice({
            tone: "danger",
            text: error instanceof Error ? error.message : tp(locale, "errorGeneric"),
          });
        }
      });
    return () => controller.abort();
  }, [applyProvider, locale]);

  const currentProvider = settings?.providers.find((p) => p.id === providerId);

  const handleLoadModels = async () => {
    if (!currentProvider) return;
    setLoadingModels(true);
    setNotice(null);
    try {
      const res = await listProviderModels(currentProvider.id, apiKey.trim());
      setModels(res.models ?? []);
    } catch (error: unknown) {
      setNotice({
        tone: "danger",
        text: error instanceof Error ? error.message : tp(locale, "errorGeneric"),
      });
    } finally {
      setLoadingModels(false);
    }
  };

  const handleSave = async (event: FormEvent) => {
    event.preventDefault();
    if (!currentProvider) return;
    setSaving(true);
    setNotice(null);
    try {
      await updateAISettings({
        provider: currentProvider.id,
        api_key: apiKey.trim() || undefined,
        clear_api_key: clearApiKey,
        model: model.trim(),
        reasoning_effort: currentProvider.supports_reasoning ? reasoning : "",
        max_output_tokens: Number(maxTokens) || 5000,
      });
      setNotice({ tone: "good", text: tp(locale, "saved") });
      await loadSettings();
    } catch (error: unknown) {
      setNotice({
        tone: "danger",
        text: error instanceof Error ? error.message : tp(locale, "errorGeneric"),
      });
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className="panel" aria-labelledby="admin-ai-heading">
      <h2 className="panel__title" id="admin-ai-heading">
        {tp(locale, "adminAiTitle")}
      </h2>
      {settings && currentProvider ? (
        <form className="field-grid" onSubmit={handleSave}>
          <div className="field">
            <label className="field__label" htmlFor="cfg-ai-provider">
              {tp(locale, "adminAiProvider")}
            </label>
            <select
              id="cfg-ai-provider"
              className="field__select"
              value={providerId}
              onChange={(event) => applyProvider(settings, event.target.value)}
            >
              {settings.providers.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.display_name}
                </option>
              ))}
            </select>
          </div>

          <div className="field">
            <label className="field__label" htmlFor="cfg-ai-key">
              {tp(locale, "adminAiKey")}
              <span
                className={
                  currentProvider.configured ? "pill pill--good" : "pill"
                }
              >
                {currentProvider.configured
                  ? tp(locale, "adminAiKeyConfigured")
                  : tp(locale, "adminAiKeyMissing")}
              </span>
            </label>
            <input
              id="cfg-ai-key"
              type="password"
              className="field__input"
              autoComplete="off"
              placeholder={currentProvider.api_key_placeholder}
              value={apiKey}
              onChange={(event) => setApiKey(event.target.value)}
            />
            <label className="field__checkbox">
              <input
                type="checkbox"
                checked={clearApiKey}
                onChange={(event) => setClearApiKey(event.target.checked)}
              />
              {tp(locale, "adminAiClearKey")}
            </label>
          </div>

          <div className="field">
            <label className="field__label" htmlFor="cfg-ai-model">
              {tp(locale, "adminAiModel")}
            </label>
            <div className="field__row-inline">
              <input
                id="cfg-ai-model"
                className="field__input"
                list="cfg-ai-models"
                value={model}
                onChange={(event) => setModel(event.target.value)}
              />
              <datalist id="cfg-ai-models">
                {models.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.display_name || m.id}
                  </option>
                ))}
              </datalist>
              <button
                type="button"
                className="btn btn--small"
                disabled={loadingModels}
                onClick={() => void handleLoadModels()}
              >
                {loadingModels
                  ? tp(locale, "loading")
                  : tp(locale, "adminAiLoadModels")}
              </button>
            </div>
          </div>

          {currentProvider.supports_reasoning ? (
            <div className="field">
              <label className="field__label" htmlFor="cfg-ai-reasoning">
                {tp(locale, "adminAiReasoning")}
              </label>
              <select
                id="cfg-ai-reasoning"
                className="field__select"
                value={reasoning}
                onChange={(event) => setReasoning(event.target.value)}
              >
                {currentProvider.reasoning_efforts.map((effort) => (
                  <option key={effort} value={effort}>
                    {effort}
                  </option>
                ))}
              </select>
            </div>
          ) : null}

          <div className="field">
            <label className="field__label" htmlFor="cfg-ai-max-tokens">
              {tp(locale, "adminAiMaxTokens")}
            </label>
            <input
              id="cfg-ai-max-tokens"
              type="number"
              className="field__input"
              min={200}
              max={128000}
              value={maxTokens}
              onChange={(event) => setMaxTokens(event.target.value)}
            />
          </div>

          <div className="btn-row">
            <button
              type="submit"
              className="btn btn--primary"
              disabled={saving}
            >
              {saving ? tp(locale, "saving") : tp(locale, "save")}
            </button>
          </div>
          {notice ? (
            <div className={`notice notice--${notice.tone}`}>{notice.text}</div>
          ) : null}
        </form>
      ) : (
        <p className="subpage__muted">{tp(locale, "loading")}</p>
      )}
    </section>
  );
}

export function AdminUsersPanel({
  locale,
  users,
  onReloadUsers,
}: {
  locale: Locale;
  users: AdminUser[];
  onReloadUsers: () => Promise<void>;
}) {
  const [revealedKeys, setRevealedKeys] = useState<Record<string, string>>({});
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [isAdmin, setIsAdmin] = useState(false);
  const [creating, setCreating] = useState(false);
  const [notice, setNotice] = useState<{ tone: "good" | "danger"; text: string } | null>(null);

  const toggleReveal = async (targetUser: string) => {
    if (revealedKeys[targetUser]) {
      setRevealedKeys((prev) => {
        const next = { ...prev };
        delete next[targetUser];
        return next;
      });
      return;
    }
    try {
      const res = await getUserAPIKey(targetUser);
      setRevealedKeys((prev) => ({ ...prev, [targetUser]: res.api_key }));
    } catch (error: unknown) {
      setNotice({
        tone: "danger",
        text: error instanceof Error ? error.message : tp(locale, "errorGeneric"),
      });
    }
  };

  const handleCreateUser = async (event: FormEvent) => {
    event.preventDefault();
    if (!username.trim()) {
      setNotice({ tone: "danger", text: tp(locale, "adminUsersRequired") });
      return;
    }
    setCreating(true);
    setNotice(null);
    try {
      const res = await createUser({
        username: username.trim(),
        email: email.trim() || undefined,
        password: password || undefined,
        IsAdmin: isAdmin,
      });
      const createdKey = res.user.APIKey || res.user.api_key;
      setNotice({
        tone: "good",
        text: createdKey
          ? `${tp(locale, "adminUsersCreated")} API key: ${createdKey}`
          : tp(locale, "adminUsersCreated"),
      });
      setUsername("");
      setEmail("");
      setPassword("");
      setIsAdmin(false);
      await onReloadUsers();
    } catch (error: unknown) {
      setNotice({
        tone: "danger",
        text: error instanceof Error ? error.message : tp(locale, "errorGeneric"),
      });
    } finally {
      setCreating(false);
    }
  };

  return (
    <section className="panel" aria-labelledby="admin-users-heading">
      <div className="panel__header">
        <h2 className="panel__title" id="admin-users-heading">
          {tp(locale, "adminUsersTitle")}
        </h2>
        <button
          type="button"
          className="btn btn--small"
          onClick={() => void onReloadUsers()}
        >
          {tp(locale, "refresh")}
        </button>
      </div>

      {users.length === 0 ? (
        <p className="subpage__muted">{tp(locale, "adminUsersEmpty")}</p>
      ) : (
        <div className="table-scroll">
          <table className="data-table">
            <thead>
              <tr>
                <th>{tp(locale, "adminUsersColUsername")}</th>
                <th>{tp(locale, "adminUsersColEmail")}</th>
                <th>{tp(locale, "adminUsersColSchema")}</th>
                <th>{tp(locale, "adminUsersColApiKey")}</th>
                <th>{tp(locale, "adminUsersColRole")}</th>
              </tr>
            </thead>
            <tbody>
              {users.map((user) => (
                <tr key={user.username}>
                  <td>{user.username}</td>
                  <td>{user.email || "—"}</td>
                  <td>
                    <code>{user.schema_name}</code>
                  </td>
                  <td>
                    <div className="btn-row">
                      <code>
                        {revealedKeys[user.username] || user.api_key_masked || "—"}
                      </code>
                      <button
                        type="button"
                        className="btn btn--small"
                        onClick={() => void toggleReveal(user.username)}
                      >
                        {revealedKeys[user.username]
                          ? tp(locale, "adminUsersHide")
                          : tp(locale, "adminUsersReveal")}
                      </button>
                    </div>
                  </td>
                  <td>
                    <span className={user.is_admin ? "pill pill--good" : "pill"}>
                      {user.is_admin
                        ? tp(locale, "adminUsersRoleAdmin")
                        : tp(locale, "adminUsersRoleUser")}
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <hr style={{ border: 0, borderTop: "1px solid var(--border)" }} />

      <form className="field-grid" onSubmit={handleCreateUser}>
        <strong>{tp(locale, "adminUsersAddTitle")}</strong>
        <div className="field-grid field-grid--two">
          <div className="field">
            <label className="field__label" htmlFor="new-username">
              {tp(locale, "adminUsersUsername")}
            </label>
            <input
              id="new-username"
              className="field__input"
              autoComplete="off"
              placeholder="alice"
              value={username}
              onChange={(event) => setUsername(event.target.value)}
            />
          </div>
          <div className="field">
            <label className="field__label" htmlFor="new-email">
              {tp(locale, "adminUsersEmail")}
            </label>
            <input
              id="new-email"
              type="email"
              className="field__input"
              autoComplete="off"
              placeholder="alice@example.com"
              value={email}
              onChange={(event) => setEmail(event.target.value)}
            />
          </div>
        </div>
        <div className="field-grid field-grid--two">
          <div className="field">
            <label className="field__label" htmlFor="new-password">
              {tp(locale, "adminUsersPassword")}
            </label>
            <input
              id="new-password"
              type="password"
              className="field__input"
              autoComplete="new-password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
            />
          </div>
          <div className="field" style={{ justifyContent: "flex-end" }}>
            <label className="field__checkbox">
              <input
                type="checkbox"
                checked={isAdmin}
                onChange={(event) => setIsAdmin(event.target.checked)}
              />
              {tp(locale, "adminUsersIsAdmin")}
            </label>
          </div>
        </div>
        <div className="btn-row">
          <button
            type="submit"
            className="btn btn--primary btn--small"
            disabled={creating}
          >
            {creating ? tp(locale, "saving") : tp(locale, "adminUsersAddBtn")}
          </button>
        </div>
        {notice ? (
          <div className={`notice notice--${notice.tone}`}>{notice.text}</div>
        ) : null}
      </form>
    </section>
  );
}
