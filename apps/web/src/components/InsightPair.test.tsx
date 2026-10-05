import { render, screen } from "@testing-library/react";

import { InsightPair } from "./InsightPair";

describe("InsightPair", () => {
  it("localizes both labels and marks a tenant preview", () => {
    render(<InsightPair locale="ru" observation="Факт" preview ai={{
      text: "Мнение", stance: "qualify", alternative_action: "", fact_ids: [], evidence_ids: [],
    }} />);

    expect(screen.getByText("Инсайт сервера")).toBeInTheDocument();
    expect(screen.getByText("AI-инсайт")).toBeInTheDocument();
    expect(screen.getByText("Предпросмотр AI")).toBeInTheDocument();
  });

  it("keeps previous prose visible while replacement fails", () => {
    render(<InsightPair locale="ru" observation="Текущий факт" state="failed" ai={{
      text: "Предыдущее мнение", stance: "qualify", fact_ids: [], evidence_ids: [],
      stale: true, source_date: "2026-10-04",
    }} />);
    expect(screen.getByText("Предыдущее мнение")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("2026-10-04 · Обновление не удалось");
    expect(screen.getByText("Текущий факт")).toBeInTheDocument();
  });

  it("does not label a non-preview opinion as a preview", () => {
    render(<InsightPair locale="sr" observation="Činjenica" />);

    expect(screen.getByText("Serverski uvid")).toBeInTheDocument();
    expect(screen.queryByText("AI pregled")).not.toBeInTheDocument();
  });

  it("renders a legible partial-data explanation when no AI opinion is available", () => {
    render(<InsightPair locale="en" observation="Sleep data are partial." state="disabled" aiUnavailableReason="Sleep data are partial. AI Insight is unavailable here; the Server Insight remains the reliable view." />);

    expect(screen.getByText("Sleep data are partial. AI Insight is unavailable here; the Server Insight remains the reliable view.")).toHaveClass("insight-pair__unavailable");
  });
});
