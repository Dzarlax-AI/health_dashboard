import { Monitor, Moon, Sun, type LucideIcon } from "lucide-react";
import { useLayoutEffect, useState, type KeyboardEvent } from "react";

import { translate, type Locale, type MessageKey } from "../i18n";

type ThemePreference = "system" | "light" | "dark";

interface ThemeSwitcherProps {
  locale: Locale;
}

interface ThemeOption {
  icon: LucideIcon;
  label: MessageKey;
  value: ThemePreference;
}

const themeOptions: ThemeOption[] = [
  { icon: Monitor, label: "themeSystem", value: "system" },
  { icon: Sun, label: "themeLight", value: "light" },
  { icon: Moon, label: "themeDark", value: "dark" },
];

function readThemePreference(): ThemePreference {
  try {
    const stored = globalThis.localStorage.getItem("theme");
    return stored === "light" || stored === "dark" ? stored : "system";
  } catch {
    return "system";
  }
}

function applyTheme(preference: ThemePreference, systemIsDark: boolean): void {
  const useDark = preference === "dark" || (preference === "system" && systemIsDark);
  globalThis.document.documentElement.toggleAttribute("dark-mode", useDark);
}

export function ThemeSwitcher({ locale }: ThemeSwitcherProps) {
  const [preference, setPreference] = useState<ThemePreference>(readThemePreference);

  useLayoutEffect(() => {
    const media = globalThis.matchMedia("(prefers-color-scheme: dark)");
    const applySystemPreference = () => applyTheme(preference, media.matches);

    applySystemPreference();
    if (preference !== "system") {
      return undefined;
    }

    media.addEventListener("change", applySystemPreference);
    return () => media.removeEventListener("change", applySystemPreference);
  }, [preference]);

  function selectTheme(nextPreference: ThemePreference): void {
    try {
      if (nextPreference === "system") {
        globalThis.localStorage.removeItem("theme");
      } else {
        globalThis.localStorage.setItem("theme", nextPreference);
      }
    } catch {
      // The visual choice still applies for this page when storage is blocked.
    }
    setPreference(nextPreference);
  }

  function handleKeyDown(
    event: KeyboardEvent<HTMLButtonElement>,
    currentIndex: number,
  ): void {
    let nextIndex: number | undefined;
    if (event.key === "ArrowRight" || event.key === "ArrowDown") {
      nextIndex = (currentIndex + 1) % themeOptions.length;
    } else if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
      nextIndex = (currentIndex - 1 + themeOptions.length) % themeOptions.length;
    } else if (event.key === "Home") {
      nextIndex = 0;
    } else if (event.key === "End") {
      nextIndex = themeOptions.length - 1;
    }

    if (nextIndex === undefined) {
      return;
    }

    event.preventDefault();
    selectTheme(themeOptions[nextIndex].value);
    const options = event.currentTarget.parentElement?.querySelectorAll<HTMLButtonElement>(
      '[role="radio"]',
    );
    options?.[nextIndex]?.focus();
  }

  return (
    <div
      aria-label={translate(locale, "themeNav")}
      className="theme-switcher"
      role="radiogroup"
    >
      {themeOptions.map(({ icon: Icon, label, value }, index) => {
        const optionLabel = translate(locale, label);
        return (
          <button
            aria-checked={preference === value}
            aria-label={optionLabel}
            key={value}
            onClick={() => selectTheme(value)}
            onKeyDown={(event) => handleKeyDown(event, index)}
            role="radio"
            tabIndex={preference === value ? 0 : -1}
            title={optionLabel}
            type="button"
          >
            <Icon aria-hidden="true" size={16} strokeWidth={1.9} />
          </button>
        );
      })}
    </div>
  );
}
