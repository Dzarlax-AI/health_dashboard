import { fireEvent, render, screen } from "@testing-library/react";

import { ThemeSwitcher } from "./ThemeSwitcher";

describe("ThemeSwitcher", () => {
  afterEach(() => {
    globalThis.localStorage.clear();
    document.documentElement.removeAttribute("dark-mode");
  });

  it("persists explicit choices and removes the override for system mode", () => {
    render(<ThemeSwitcher locale="en" />);

    const system = screen.getByRole("radio", { name: "Use system theme" });
    const light = screen.getByRole("radio", { name: "Use light theme" });
    const dark = screen.getByRole("radio", { name: "Use dark theme" });

    expect(system).toHaveAttribute("aria-checked", "true");

    fireEvent.click(dark);
    expect(document.documentElement).toHaveAttribute("dark-mode");
    expect(globalThis.localStorage.getItem("theme")).toBe("dark");

    fireEvent.click(light);
    expect(document.documentElement).not.toHaveAttribute("dark-mode");
    expect(globalThis.localStorage.getItem("theme")).toBe("light");

    fireEvent.click(system);
    expect(document.documentElement).not.toHaveAttribute("dark-mode");
    expect(globalThis.localStorage.getItem("theme")).toBeNull();
  });

  it("localizes its accessible labels", () => {
    render(<ThemeSwitcher locale="ru" />);

    expect(screen.getByRole("radiogroup", { name: "Тема" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Системная тема" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Светлая тема" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Тёмная тема" })).toBeInTheDocument();
  });
});
