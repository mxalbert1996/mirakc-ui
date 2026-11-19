import { afterEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import WatchPlayer from "./Player.tsx";
import { sampleLiveComments } from "../../../lib/fixtures.ts";
import { t } from "../../../locales/i18n.ts";

afterEach(() => {
  vi.unstubAllGlobals();
});

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

// チケット付与の検証は上記 pending ローダーでは止まってしまうため、解決する
// フェイクで init() を最後まで実行し、mpegts.createPlayer へ渡った URL を見る。
describe("WatchPlayer チケット", () => {
  function fakeMpegts() {
    const createPlayer = vi.fn(() => ({
      on: vi.fn(),
      attachMediaElement: vi.fn(),
      load: vi.fn(),
      play: vi.fn(() => Promise.resolve()),
      pause: vi.fn(),
      unload: vi.fn(),
      detachMediaElement: vi.fn(),
      destroy: vi.fn(),
    }));
    const loader = () =>
      Promise.resolve({
        default: {
          isSupported: () => true,
          createPlayer,
          Events: {
            ERROR: "ERROR",
            PES_PRIVATE_DATA_ARRIVED: "PES_PRIVATE_DATA_ARRIVED",
            TIMED_ID3_METADATA_ARRIVED: "TIMED_ID3_METADATA_ARRIVED",
          },
        },
      });
    return { createPlayer, loader };
  }

  const fakeAribb24 = () =>
    Promise.resolve({
      Controller: class {
        attachFeeder() {}
        attachRenderer() {}
        attachMedia() {}
        show() {}
        hide() {}
        detachMedia() {}
        detachFeeder() {}
        detachRenderer() {}
      },
      MPEGTSFeeder: class {},
      CanvasMainThreadRenderer: class {
        destroy() {}
      },
    });

  function stubTicket(ticket: string | null) {
    const fetchMock = vi.fn(() =>
      Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({ ticket }),
      })
    );
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  /**
   * チケット取得を保留させて手動で解決するモック。解決前に unmount 等を行う
   * 破棄レースの検証に使う。
   */
  function stubDeferredTicketFetch() {
    const resolvers: Array<(ticket: string | null) => void> = [];
    const fetchMock = vi.fn(() =>
      new Promise((resolve) => {
        resolvers.push((ticket) =>
          resolve({
            ok: true,
            status: 200,
            json: () => Promise.resolve({ ticket }),
          })
        );
      })
    );
    vi.stubGlobal("fetch", fetchMock);
    return {
      fetchMock,
      /** index 番目の呼び出しを ticket で解決する。 */
      resolve: (index: number, ticket: string | null) => {
        resolvers[index]?.(ticket);
      },
    };
  }

  async function flush() {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
  }

  function ticketProps(
    loader: ReturnType<typeof fakeMpegts>["loader"],
    streamUrl: string,
  ): Props {
    return baseProps({
      streamUrl,
      loadMpegts: loader as unknown as NonNullable<Props["loadMpegts"]>,
      loadAribb24: fakeAribb24 as unknown as NonNullable<
        Props["loadAribb24"]
      >,
    });
  }

  it("チケットが返れば既存クエリの後に ticket を付けて mpegts へ渡す", async () => {
    stubTicket("abc123");
    const { createPlayer, loader } = fakeMpegts();
    render(
      <WatchPlayer
        {...ticketProps(
          loader,
          "/api/transcode/services/1024?audioTrack=0&quality=720p",
        )}
      />,
    );
    await flush();
    expect(createPlayer).toHaveBeenCalledWith(
      expect.objectContaining({
        url:
          "/api/transcode/services/1024?audioTrack=0&quality=720p&ticket=abc123",
      }),
      expect.anything(),
    );
  });

  it("チケットが null なら streamUrl はそのまま (認証無効)", async () => {
    stubTicket(null);
    const { createPlayer, loader } = fakeMpegts();
    render(
      <WatchPlayer {...ticketProps(loader, "/api/transcode/services/1024")} />,
    );
    await flush();
    expect(createPlayer).toHaveBeenCalledWith(
      expect.objectContaining({ url: "/api/transcode/services/1024" }),
      expect.anything(),
    );
  });

  it("streamUrl 再構築 (画質/音声切替) でチケットを取り直す", async () => {
    const fetchMock = stubTicket("t1");
    const { createPlayer, loader } = fakeMpegts();
    const { rerender } = render(
      <WatchPlayer
        {...ticketProps(loader, "/api/transcode/services/1024")}
      />,
    );
    await flush();
    rerender(
      <WatchPlayer
        {...ticketProps(
          loader,
          "/api/transcode/services/1024?audioTrack=1&quality=1080p",
        )}
      />,
    );
    await flush();
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(createPlayer).toHaveBeenLastCalledWith(
      expect.objectContaining({
        url:
          "/api/transcode/services/1024?audioTrack=1&quality=1080p&ticket=t1",
      }),
      expect.anything(),
    );
  });

  it("チケット取得中に unmount しても createPlayer を呼ばない", async () => {
    const { fetchMock, resolve } = stubDeferredTicketFetch();
    const { createPlayer, loader } = fakeMpegts();
    const { unmount } = render(
      <WatchPlayer
        {...ticketProps(loader, "/api/transcode/services/1024")}
      />,
    );
    // mpegts / aribb24 モジュールは読み終え、チケット取得で init() が止まる。
    await flush();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    unmount();
    resolve(0, "t1");
    await flush();
    // destroyed ガードが解決後の初期化を止め、playerRef への代入も起きない。
    expect(createPlayer).not.toHaveBeenCalled();
  });

  it("チケット取得中に streamUrl が組み替われば旧取得を破棄して新分だけ開く", async () => {
    const { resolve } = stubDeferredTicketFetch();
    const { createPlayer, loader } = fakeMpegts();
    const { rerender } = render(
      <WatchPlayer
        {...ticketProps(loader, "/api/transcode/services/1024")}
      />,
    );
    await flush();
    rerender(
      <WatchPlayer
        {...ticketProps(
          loader,
          "/api/transcode/services/1024?audioTrack=1&quality=1080p",
        )}
      />,
    );
    // 旧 effect 分の取得を解決しても destroyed なので createPlayer しない。
    resolve(0, "old");
    await flush();
    expect(createPlayer).not.toHaveBeenCalled();
    // 新 effect 分の取得だけが新しい URL で開く。
    resolve(1, "new");
    await flush();
    expect(createPlayer).toHaveBeenCalledTimes(1);
    expect(createPlayer).toHaveBeenCalledWith(
      expect.objectContaining({
        url:
          "/api/transcode/services/1024?audioTrack=1&quality=1080p&ticket=new",
      }),
      expect.anything(),
    );
  });
});
