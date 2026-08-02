import { render, screen } from "@testing-library/react";

import { AppHeader } from "./AppHeader";

describe("AppHeader", () => {
  afterEach(() => {
    window.history.replaceState({}, "", "/");
  });

  it("logs out with the server-required POST request", () => {
    render(<AppHeader locale="en" />);

    const button = screen.getByRole("button", { name: "Log out" });
    expect(button.closest("form")).toHaveAttribute("method", "post");
    expect(button.closest("form")).toHaveAttribute("action", "/logout");
  });

  it("preserves query parameters independently for each locale link", () => {
    window.history.replaceState({}, "", "/?fixture=partial&lang=en");
    render(<AppHeader locale="en" />);

    expect(screen.getByRole("link", { name: "RU" })).toHaveAttribute(
      "href",
      "/?fixture=partial&lang=ru",
    );
    expect(screen.getByRole("link", { name: "SR" })).toHaveAttribute(
      "href",
      "/?fixture=partial&lang=sr",
    );
  });
});
