import { useLayoutEffect, useState } from "react";
import Icon from "../components/atoms/Icon.tsx";

type ColorScheme = "light" | "dark" | "auto";

// client/index.html の初期化スクリプトと同じキー / 値の契約。
const STORAGE_KEY = "color-scheme";

function getStoredScheme(): ColorScheme {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored === "light" || stored === "dark") {
      return stored;
    }
  } catch {
    // localStorage unavailable
  }
  return "auto";
}

function applyScheme(scheme: ColorScheme) {
  if (scheme === "auto") {
    delete document.documentElement.dataset.theme;
  } else {
    document.documentElement.dataset.theme = scheme;
  }
}

const iconMap: Record<ColorScheme, string> = {
  light: "light_mode",
  dark: "dark_mode",
  auto: "contrast",
};

const nextMap: Record<ColorScheme, ColorScheme> = {
  auto: "light",
  light: "dark",
  dark: "auto",
};

export default function ColorSchemeToggle() {
  const [scheme, setScheme] = useState<ColorScheme>(getStoredScheme);

  useLayoutEffect(() => {
    applyScheme(scheme);
  }, [scheme]);

  const toggle = () => {
    const next = nextMap[scheme];
    setScheme(next);
    try {
      if (next === "auto") {
        localStorage.removeItem(STORAGE_KEY);
      } else {
        localStorage.setItem(STORAGE_KEY, next);
      }
    } catch {
      // localStorage unavailable
    }
  };

  return (
    <button
      type="button"
      onClick={toggle}
      className="color-scheme-toggle"
      aria-label={`Color scheme: ${scheme}`}
    >
      <Icon>{iconMap[scheme]}</Icon>
    </button>
  );
}
