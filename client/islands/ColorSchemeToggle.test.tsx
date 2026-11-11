import { beforeEach, describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import ColorSchemeToggle from "./ColorSchemeToggle.tsx";

const STORAGE_KEY = "color-scheme";

beforeEach(() => {
  localStorage.clear();
  delete document.documentElement.dataset.theme;
});

describe("ColorSchemeToggle", () => {
  it("保存済みの dark を初期状態として適用する", () => {
    localStorage.setItem(STORAGE_KEY, "dark");

    render(<ColorSchemeToggle />);

    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(
      screen.getByRole("button", { name: "Color scheme: dark" }),
    ).toBeTruthy();
  });

  it("dark からクリックすると auto に戻る", () => {
    localStorage.setItem(STORAGE_KEY, "dark");
    render(<ColorSchemeToggle />);

    fireEvent.click(screen.getByRole("button"));

    expect(document.documentElement.dataset.theme).toBeUndefined();
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull();
  });

  it("不正な保存値は auto として扱う", () => {
    localStorage.setItem(STORAGE_KEY, "purple");

    render(<ColorSchemeToggle />);

    expect(document.documentElement.dataset.theme).toBeUndefined();
    expect(
      screen.getByRole("button", { name: "Color scheme: auto" }),
    ).toBeTruthy();
  });

  it("auto からクリックすると light を保存する", () => {
    render(<ColorSchemeToggle />);

    fireEvent.click(screen.getByRole("button"));

    expect(document.documentElement.dataset.theme).toBe("light");
    expect(localStorage.getItem(STORAGE_KEY)).toBe("light");
  });
});
