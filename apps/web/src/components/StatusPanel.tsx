interface StatusPanelProps {
  state: "loading" | "partial" | "stale" | "unavailable" | "error";
  title: string;
  detail: string;
}

export function StatusPanel({ state, title, detail }: StatusPanelProps) {
  return (
    <section className={`status-panel status-panel--${state}`} data-resource-state={state}>
      <span className="status-panel__indicator" aria-hidden="true" />
      <div>
        <h2>{title}</h2>
        <p>{detail}</p>
      </div>
    </section>
  );
}
