import { render, screen } from "@testing-library/react";
import { sleepFixtureResources } from "./fixtures";
import { SleepComparisonCard } from "./SleepComparisonCard";

it("explains the recorded source choice and leaves older comparisons without a reason", () => {
  const original = sleepFixtureResources("ru").comparison!;
  const fallback = { ...original, source_selection_reason: "preferred_unavailable", preferred_sleep_source: "Часы №2" };
  const view = render(<SleepComparisonCard value={fallback} locale="ru" onToday />);
  expect(screen.getByText(/Часы №2.*подходящей записи/)).toBeInTheDocument();
  expect(screen.getByText(/Источник записи.*Synthetic Watch/)).toBeInTheDocument();
  view.rerender(<SleepComparisonCard value={{ ...fallback, source: "Часы №2" }} locale="ru" onToday />);
  expect(screen.getByText(/Источник записи.*Часы №2/)).toBeInTheDocument();
  expect(screen.getByText(/Health выбрал источник автоматически/)).toBeInTheDocument();
  expect(screen.queryByText(/другой источник/)).not.toBeInTheDocument();
  view.rerender(<SleepComparisonCard value={original} locale="ru" onToday />);
  expect(screen.queryByText(/Часы №2|автоматически|выбрано в ваших настройках/)).not.toBeInTheDocument();
});
