import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { HEVC_STORAGE_KEY } from "../../../lib/hevc-preference.ts";
import PlaybackSettings from "./PlaybackSettings.tsx";
import { t } from "../../../locales/i18n.ts";

const label = t("settings.playback.hevc.label");

function stubHevcSupport(supported: boolean) {
  vi.stubGlobal(
    "MediaSource",
    supported ? { isTypeSupported: () => true } : undefined,
  );
}

beforeEach(() => {
  localStorage.clear();
  stubHevcSupport(true);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("PlaybackSettings", () => {
  it("既定ではオンで、保存値を持たない", () => {
    render(<PlaybackSettings />);

    const sw = screen.getByRole("switch", { name: label });
    expect(sw.getAttribute("aria-checked")).toBe("true");
    expect(localStorage.getItem(HEVC_STORAGE_KEY)).toBeNull();
    expect(screen.getByText(t("settings.playback.hevc.description")))
      .toBeTruthy();
  });

  it("オフにすると保存し、再度オンにすると保存値を消す", () => {
    render(<PlaybackSettings />);

    const sw = screen.getByRole("switch", { name: label });
    fireEvent.click(sw);
    expect(sw.getAttribute("aria-checked")).toBe("false");
    expect(localStorage.getItem(HEVC_STORAGE_KEY)).toBe("0");

    fireEvent.click(sw);
    expect(sw.getAttribute("aria-checked")).toBe("true");
    expect(localStorage.getItem(HEVC_STORAGE_KEY)).toBeNull();
  });

  it("保存済みのオフを初期状態として反映する", () => {
    localStorage.setItem(HEVC_STORAGE_KEY, "0");

    render(<PlaybackSettings />);

    expect(
      screen.getByRole("switch", { name: label }).getAttribute("aria-checked"),
    ).toBe("false");
  });

  it("非対応ブラウザでは無効化し、非対応の案内を出す", () => {
    stubHevcSupport(false);

    render(<PlaybackSettings />);

    const sw = screen.getByRole("switch", { name: label }) as HTMLButtonElement;
    expect(sw.disabled).toBe(true);
    expect(sw.getAttribute("aria-checked")).toBe("false");
    expect(screen.getByText(t("settings.playback.hevc.unsupported")))
      .toBeTruthy();
    expect(screen.queryByText(t("settings.playback.hevc.description")))
      .toBeNull();
  });

  it("非対応ブラウザでは既定のオン状態でもオフ表示にする", () => {
    stubHevcSupport(false);

    render(<PlaybackSettings />);

    expect(
      screen.getByRole("switch", { name: label }).getAttribute("aria-checked"),
    ).toBe("false");
  });
});
