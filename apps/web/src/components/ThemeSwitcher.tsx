import { Monitor, Moon, Sun, type LucideIcon } from "lucide-react";
import { useLayoutEffect, useState } from "react";

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

  return (
    <div
      aria-label={translate(locale, "themeNav")}
      className="theme-switcher"
      role="radiogroup"
    >
      {themeOptions.map(({ icon: Icon, label, value }) => {
        const optionLabel = translate(locale, label);
        return (
          <button
            aria-checked={preference === value}
            aria-label={optionLabel}
            key={value}
            onClick={() => selectTheme(value)}
            role="radio"
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
