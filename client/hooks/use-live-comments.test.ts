import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, renderHook } from "@testing-library/react";
import { toLiveComment, useLiveComments } from "./use-live-comments.ts";
import { formatHm } from "../lib/datetime.ts";
import type {
  CommentSourceId,
  SourceComment,
} from "../../server/lib/comments/types.ts";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

class FakeEventSource {
  static instances: FakeEventSource[] = [];

  listeners = new Map<string, Set<(event: MessageEvent) => void>>();
  onerror: ((event: Event) => void) | null = null;
  closed = false;

  constructor(readonly url: string) {
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, listener: (event: MessageEvent) => void) {
    if (!this.listeners.has(type)) {
      this.listeners.set(type, new Set());
    }
    this.listeners.get(type)!.add(listener);
  }

  close() {
    this.closed = true;
  }

  emit(type: string, data: string) {
    for (const listener of this.listeners.get(type) ?? []) {
      listener(new MessageEvent(type, { data }));
    }
  }
}

/** POST /api/auth/ticket のモック応答。 */
type TicketResponse = {
  ok: true;
  status: 200;
  json: () => Promise<{ ticket: string | null }>;
};

function ticketResponseOf(ticket: string | null): TicketResponse {
  return { ok: true, status: 200, json: () => Promise.resolve({ ticket }) };
}

/**
 * POST /api/auth/ticket をモックする。tickets は呼び出し順の戻り値で、
 * 少ない場合は末尾の値を使い続ける。
 */
function stubTicketFetch(tickets: Array<string | null>) {
  let calls = 0;
  const fetchMock = vi.fn((): Promise<TicketResponse> => {
    const ticket = tickets[Math.min(calls++, tickets.length - 1)];
    return Promise.resolve(ticketResponseOf(ticket));
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

/**
 * チケット取得を保留させて手動で解決するモック。解決前に unmount 等を行う
 * 破棄レースの検証に使う。
 */
function stubDeferredTicketFetch() {
  const resolvers: Array<(response: TicketResponse) => void> = [];
  const fetchMock = vi.fn((): Promise<TicketResponse> =>
    new Promise((resolve) => resolvers.push(resolve))
  );
  vi.stubGlobal("fetch", fetchMock);
  return {
    fetchMock,
    /** index 番目の呼び出しを ticket で解決する。 */
    resolve: (index: number, ticket: string | null) => {
      resolvers[index]?.(ticketResponseOf(ticket));
    },
  };
}

function setup(
  serviceId: number | undefined,
  opts: {
    limit?: number;
    tickets?: Array<string | null>;
    fetchMock?: ReturnType<typeof stubTicketFetch>;
  } = {},
) {
  FakeEventSource.instances = [];
  const fetchMock = opts.fetchMock ?? stubTicketFetch(opts.tickets ?? [null]);
  const createEventSource = (url: string) =>
    new FakeEventSource(url) as unknown as EventSource;
  const view = renderHook(
    (props: { serviceId: number | undefined }) =>
      useLiveComments(props.serviceId, {
        createEventSource,
        limit: opts.limit,
      }),
    { initialProps: { serviceId } },
  );
  return { view, instances: FakeEventSource.instances, fetchMock };
}

/** チケット取得の microtask を流して EventSource 生成まで進める。 */
async function flush() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

function commentOf(
  id: string,
  source: CommentSourceId,
  text: string,
): SourceComment {
  return { id, source, at: 1718240401_000, text };
}

describe("useLiveComments", () => {
  it("serviceId が無ければ接続しない", async () => {
    const { view, instances, fetchMock } = setup(undefined);
    await flush();
    expect(instances.length).toBe(0);
    expect(fetchMock).not.toHaveBeenCalled();
    expect(view.result.current.connected).toBe(false);
  });

  it("チケットが返れば URL に ?ticket= を付けて開く", async () => {
    const { instances, fetchMock } = setup(10, { tickets: ["abc123"] });
    await flush();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(instances[0].url).toBe(
      "/api/comments/services/10/stream?ticket=abc123",
    );
  });

  it("チケットが null なら URL はそのまま (認証無効)", async () => {
    const { instances } = setup(10);
    await flush();
    expect(instances[0].url).toBe("/api/comments/services/10/stream");
  });

  it("EventSource 再生成のたびに新たなチケットを取得する", async () => {
    const { view, instances, fetchMock } = setup(10, {
      tickets: ["t1", "t2"],
    });
    await flush();
    view.rerender({ serviceId: 20 });
    await flush();
    expect(instances.length).toBe(2);
    expect(instances[0].closed).toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(instances[1].url).toBe("/api/comments/services/20/stream?ticket=t2");
  });

  it("チケット取得中に unmount しても EventSource を開かない", async () => {
    const { fetchMock, resolve } = stubDeferredTicketFetch();
    const { view, instances } = setup(10, { fetchMock });
    await flush();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    view.unmount();
    resolve(0, "t1");
    await flush();
    // cancelled ガードが解決後の open を止める。EventSource が無ければ
    // unmount 後に状態を書く経路も存在しない。
    expect(instances.length).toBe(0);
    expect(view.result.current.connected).toBe(false);
  });

  it("チケット取得中に serviceId が変われば旧取得を破棄して新分だけ開く", async () => {
    const { fetchMock, resolve } = stubDeferredTicketFetch();
    const { view, instances } = setup(10, { fetchMock });
    await flush();
    view.rerender({ serviceId: 20 });
    // 旧 effect 分の取得を解決しても cancelled なので何も開かない。
    resolve(0, "old");
    await flush();
    expect(instances.length).toBe(0);
    // 新 effect 分の取得だけが stream を開く。
    resolve(1, "new");
    await flush();
    expect(instances.length).toBe(1);
    expect(instances[0].url).toBe(
      "/api/comments/services/20/stream?ticket=new",
    );
  });

  it("comment を取得元付きの LiveComment に変換して積む", async () => {
    const { view, instances } = setup(10);
    await flush();
    act(() => {
      instances[0].emit("sources", JSON.stringify(["nicolive"]));
      instances[0].emit(
        "comment",
        JSON.stringify(commentOf("a", "nicolive", "やあ")),
      );
    });
    expect(view.result.current.connected).toBe(true);
    expect(view.result.current.sources).toEqual(["nicolive"]);
    expect(view.result.current.comments).toEqual([
      {
        id: "nicolive:a",
        name: "",
        colorHue: expect.any(Number),
        text: "やあ",
        time: formatHm(1718240401_000),
        me: false,
        source: "nicolive",
      },
    ]);
  });

  it("複数取得元のコメントをフィルタせず全て積む", async () => {
    const { view, instances } = setup(10);
    await flush();
    act(() => {
      instances[0].emit("sources", JSON.stringify(["nicolive", "nx-jikkyo"]));
      instances[0].emit(
        "comment",
        JSON.stringify(commentOf("a", "nicolive", "ニコ生")),
      );
      instances[0].emit(
        "comment",
        JSON.stringify(commentOf("b", "nx-jikkyo", "NX")),
      );
    });
    expect(view.result.current.sources).toEqual(["nicolive", "nx-jikkyo"]);
    expect(view.result.current.comments.map((c) => c.text)).toEqual([
      "ニコ生",
      "NX",
    ]);
  });

  it("sources が空なら実況非対応として切断する", async () => {
    const { view, instances } = setup(10);
    await flush();
    act(() => {
      instances[0].emit("sources", JSON.stringify([]));
    });
    expect(view.result.current.connected).toBe(false);
    expect(instances[0].closed).toBe(true);
  });

  it("sources 再送 (再接続) で接続を保ちコメントを捨てない", async () => {
    const { view, instances } = setup(10);
    await flush();
    act(() => {
      instances[0].emit("sources", JSON.stringify(["nicolive"]));
      instances[0].emit(
        "comment",
        JSON.stringify(commentOf("a", "nicolive", "やあ")),
      );
    });
    act(() => {
      instances[0].emit("sources", JSON.stringify(["nicolive", "nx-jikkyo"]));
    });
    expect(view.result.current.connected).toBe(true);
    expect(view.result.current.sources).toEqual(["nicolive", "nx-jikkyo"]);
    expect(view.result.current.comments.map((c) => c.text)).toEqual(["やあ"]);
  });

  it("serviceId が変わったら接続し直してコメントを捨てる", async () => {
    const { view, instances } = setup(10);
    await flush();
    act(() => {
      instances[0].emit("sources", JSON.stringify(["nicolive"]));
      instances[0].emit(
        "comment",
        JSON.stringify(commentOf("a", "nicolive", "old")),
      );
    });
    expect(view.result.current.comments.length).toBe(1);

    view.rerender({ serviceId: 20 });
    await flush();
    expect(instances[0].closed).toBe(true);
    expect(instances.length).toBe(2);
    expect(view.result.current.comments).toEqual([]);
  });
});

describe("toLiveComment", () => {
  it("source を保持し、colorHue は author から安定導出する", () => {
    const a1 = toLiveComment({
      ...commentOf("x", "nicolive", "t"),
      author: "太郎",
    });
    const a2 = toLiveComment({
      ...commentOf("y", "nicolive", "t"),
      author: "太郎",
    });
    expect(a1.source).toBe("nicolive");
    expect(a1.colorHue).toBe(a2.colorHue);
  });
});
