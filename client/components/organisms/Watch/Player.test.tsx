import { describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import WatchPlayer from "./Player.tsx";
import { sampleLiveComments } from "../../../lib/fixtures.ts";
import { t } from "../../../locales/i18n.ts";

// Player.tsx は `import("mpegts.js")` (esm.sh URL) / `import("aribb24.js")` (npm)
// を動的 import する。mpegts.js は vitest.config.ts の test.alias でローカル
// スタブに解決させ、実行は下記 pending ローダー注入で止める (二重の安全策)。

// 外部ストリームライブラリ (esm.sh の mpegts.js / npm の aribb24.js) を読み込ま
// ないため、解決しない Promise を返すローダーを注入する。これで init() は await
// で停止し、controls UI だけが同期描画される。
const pending = () => new Promise<never>(() => {});

function playerFrame(container: HTMLElement): HTMLElement {
  return container.querySelector("[data-controls-visible]") as HTMLElement;
}

type Props = Parameters<typeof WatchPlayer>[0];

function baseProps(overrides: Partial<Props> = {}): Props {
  return {
    streamUrl: "/api/transcode/services/1024",
    audioTrackIndex: 0,
    onAudioTrackChange: () => {},
    audios: [
      { componentType: 1, isMain: true, langs: ["jpn"], samplingRate: 48000 },
      { componentType: 1, isMain: false, langs: ["eng"], samplingRate: 48000 },
    ],
    quality: "720p",
    onQualityChange: () => {},
    captionVisible: true,
    onCaptionToggle: () => {},
    serviceSelectedAt: 0,
    // deno-lint-ignore no-explicit-any
    loadMpegts: pending as any,
    // deno-lint-ignore no-explicit-any
    loadAribb24: pending as any,
    ...overrides,
  };
}

describe("WatchPlayer", () => {
  it("streamUrl 未指定なら視聴選択を促す placeholder を出す", () => {
    render(<WatchPlayer {...baseProps({ streamUrl: undefined })} />);
    expect(screen.getByText(t("watch.selectService"))).toBeTruthy();
  });

  it("streamUrl 指定で controls (ミュート/字幕/全画面/音声/画質) を描画する", () => {
    render(<WatchPlayer {...baseProps()} />);
    // 初期 muted=true なので「ミュート解除」ラベル。
    expect(
      screen.getByRole("button", { name: t("watch.player.unmute") }),
    ).toBeTruthy();
    expect(
      screen.getByRole("button", { name: t("watch.player.fullscreen") }),
    ).toBeTruthy();
    expect(
      screen.getByRole("button", { name: t("watch.audio.label") }),
    ).toBeTruthy();
    expect(
      screen.getByRole("button", { name: t("watch.quality.label") }),
    ).toBeTruthy();
    // 画質トリガーには quality 値が表示される。
    expect(screen.getByText("720p")).toBeTruthy();
  });

  it("ミュートトグルで aria-label が反転する", () => {
    render(<WatchPlayer {...baseProps()} />);
    const muteBtn = screen.getByRole("button", {
      name: t("watch.player.unmute"),
    });
    fireEvent.click(muteBtn);
    // unmute 後は「ミュート」ラベルに変わる。
    expect(
      screen.getByRole("button", { name: t("watch.player.mute") }),
    ).toBeTruthy();
  });

  it("字幕ボタンで onCaptionToggle が発火する", () => {
    const onCaptionToggle = vi.fn();
    render(<WatchPlayer {...baseProps({ onCaptionToggle })} />);
    // captionVisible=true なので「字幕 OFF」ラベル。
    fireEvent.click(
      screen.getByRole("button", { name: t("watch.caption.hide") }),
    );
    expect(onCaptionToggle).toHaveBeenCalledTimes(1);
  });

  it("音声メニューを開いて副音声を選ぶと onAudioTrackChange が発火する", () => {
    const onAudioTrackChange = vi.fn();
    render(<WatchPlayer {...baseProps({ onAudioTrackChange })} />);
    fireEvent.click(
      screen.getByRole("button", { name: t("watch.audio.label") }),
    );
    // index 1 = 副音声 (eng)。メニュー項目をクリックする。
    fireEvent.click(screen.getByText(`${t("watch.audio.sub")} (eng)`));
    expect(onAudioTrackChange).toHaveBeenCalledTimes(1);
    expect(onAudioTrackChange).toHaveBeenCalledWith(1);
  });

  it("画質メニューを開いて 480p を選ぶと onQualityChange が発火する", () => {
    const onQualityChange = vi.fn();
    render(<WatchPlayer {...baseProps({ onQualityChange })} />);
    fireEvent.click(
      screen.getByRole("button", { name: t("watch.quality.label") }),
    );
    fireEvent.click(screen.getByText("480p"));
    expect(onQualityChange).toHaveBeenCalledTimes(1);
    expect(onQualityChange).toHaveBeenCalledWith("480p");
  });

  it("comments 未指定ならコメントオーバーレイと開閉タブを出さない", () => {
    render(<WatchPlayer {...baseProps()} />);
    expect(
      screen.queryByRole("button", { name: t("watch.live.overlayShow") }),
    ).toBeNull();
  });

  it("開閉タブでコメントオーバーレイが開閉する", () => {
    const { container } = render(
      <WatchPlayer {...baseProps({ comments: sampleLiveComments })} />,
    );
    // 初期状態は閉 (aria-hidden) で、タブは「表示」ラベル。
    const overlay = container.querySelector("[aria-hidden]") as HTMLElement;
    expect(overlay.getAttribute("aria-hidden")).toBe("true");

    fireEvent.click(
      screen.getByRole("button", { name: t("watch.live.overlayShow") }),
    );
    expect(overlay.getAttribute("aria-hidden")).toBe("false");
    expect(screen.getByText(sampleLiveComments[0].text)).toBeTruthy();

    fireEvent.click(
      screen.getByRole("button", { name: t("watch.live.overlayHide") }),
    );
    expect(overlay.getAttribute("aria-hidden")).toBe("true");
  });

  it("ポインタ移動で controls を表示し、操作が止まると自動で隠す", () => {
    vi.useFakeTimers();
    try {
      const { container } = render(<WatchPlayer {...baseProps()} />);
      const player = playerFrame(container);
      expect(player.getAttribute("data-controls-visible")).toBe("false");

      fireEvent.pointerMove(player);
      expect(player.getAttribute("data-controls-visible")).toBe("true");

      act(() => {
        vi.advanceTimersByTime(2600);
      });
      expect(player.getAttribute("data-controls-visible")).toBe("false");
    } finally {
      vi.useRealTimers();
    }
  });

  it("ポップメニューを開いている間は自動で隠さない", () => {
    vi.useFakeTimers();
    try {
      const { container } = render(<WatchPlayer {...baseProps()} />);
      const player = playerFrame(container);
      fireEvent.pointerMove(player);
      fireEvent.click(
        screen.getByRole("button", { name: t("watch.audio.label") }),
      );

      act(() => {
        vi.advanceTimersByTime(5200);
      });
      expect(player.getAttribute("data-controls-visible")).toBe("true");
      expect(screen.getByText(`${t("watch.audio.sub")} (eng)`)).toBeTruthy();
    } finally {
      vi.useRealTimers();
    }
  });

  it("メニューを閉じると自動非表示が再開する", () => {
    vi.useFakeTimers();
    try {
      const { container } = render(<WatchPlayer {...baseProps()} />);
      const player = playerFrame(container);
      fireEvent.pointerMove(player);
      fireEvent.click(
        screen.getByRole("button", { name: t("watch.audio.label") }),
      );

      // 開いている間は、開く前に予約されたタイマーが来ても隠れない。
      act(() => {
        vi.advanceTimersByTime(2600);
      });
      expect(player.getAttribute("data-controls-visible")).toBe("true");

      // 閉じた時点から改めて 2.6 秒を数え直す。
      fireEvent.click(screen.getByText(`${t("watch.audio.sub")} (eng)`));
      act(() => {
        vi.advanceTimersByTime(2600);
      });
      expect(player.getAttribute("data-controls-visible")).toBe("false");
    } finally {
      vi.useRealTimers();
    }
  });

  it("タッチはタップでの開閉だけに従う (移動やメニュー外クリックに反応しない)", () => {
    vi.useFakeTimers();
    try {
      const { container } = render(<WatchPlayer {...baseProps()} />);
      const player = playerFrame(container);
      const video = container.querySelector("video")!;

      fireEvent.pointerMove(player, { pointerType: "touch" });
      expect(player.getAttribute("data-controls-visible")).toBe("false");

      fireEvent.pointerDown(video, { pointerType: "touch" });
      expect(player.getAttribute("data-controls-visible")).toBe("true");

      act(() => {
        vi.advanceTimersByTime(2600);
      });
      expect(player.getAttribute("data-controls-visible")).toBe("true");

      fireEvent.pointerDown(video, { pointerType: "touch" });
      expect(player.getAttribute("data-controls-visible")).toBe("false");
    } finally {
      vi.useRealTimers();
    }
  });

  it("メニューをタッチで閉じても、その後のタップ表示は自動非表示に巻き込まれない", () => {
    vi.useFakeTimers();
    try {
      const { container } = render(<WatchPlayer {...baseProps()} />);
      const player = playerFrame(container);
      const video = container.querySelector("video")!;
      fireEvent.pointerDown(video, { pointerType: "touch" });
      fireEvent.click(
        screen.getByRole("button", { name: t("watch.audio.label") }),
      );

      fireEvent.pointerDown(video, { pointerType: "touch" });
      expect(player.getAttribute("data-controls-visible")).toBe("false");

      fireEvent.pointerDown(video, { pointerType: "touch" });
      act(() => {
        vi.advanceTimersByTime(2600);
      });
      expect(player.getAttribute("data-controls-visible")).toBe("true");
    } finally {
      vi.useRealTimers();
    }
  });

  it("Escape でメニューを閉じ、フォーカスが外れても開いたままにしない", () => {
    vi.useFakeTimers();
    try {
      const { container } = render(<WatchPlayer {...baseProps()} />);
      const player = playerFrame(container);
      fireEvent.pointerMove(player);
      fireEvent.click(
        screen.getByRole("button", { name: t("watch.audio.label") }),
      );

      fireEvent.keyDown(player, { key: "Escape" });
      expect(screen.queryByText(`${t("watch.audio.sub")} (eng)`)).toBeNull();

      act(() => {
        vi.advanceTimersByTime(2600);
      });
      expect(player.getAttribute("data-controls-visible")).toBe("false");
    } finally {
      vi.useRealTimers();
    }
  });

  it("メニュー外を押すとメニューが閉じて自動非表示に戻る", () => {
    vi.useFakeTimers();
    try {
      const { container } = render(<WatchPlayer {...baseProps()} />);
      const player = playerFrame(container);
      const video = container.querySelector("video")!;
      fireEvent.pointerMove(player);
      fireEvent.click(
        screen.getByRole("button", { name: t("watch.audio.label") }),
      );
      expect(screen.getByText(`${t("watch.audio.sub")} (eng)`)).toBeTruthy();

      fireEvent.pointerDown(video, { pointerType: "mouse" });
      expect(screen.queryByText(`${t("watch.audio.sub")} (eng)`)).toBeNull();

      act(() => {
        vi.advanceTimersByTime(2600);
      });
      expect(player.getAttribute("data-controls-visible")).toBe("false");
    } finally {
      vi.useRealTimers();
    }
  });

  it("pause されても (メディアキー等) 即座に再生へ戻し停止させない", () => {
    const { container } = render(<WatchPlayer {...baseProps()} />);
    const video = container.querySelector("video");
    expect(video).toBeTruthy();
    const playSpy = vi.spyOn(video!, "play").mockResolvedValue(undefined);
    fireEvent.pause(video!);
    expect(playSpy).toHaveBeenCalled();
  });
});
