import { describe, expect, it } from "vitest";
import { fetchSlimPrograms, fullProgramOf } from "./programs.ts";
import type { components } from "./schema.d.ts";

type MirakurunProgram = components["schemas"]["MirakurunProgram"];

const slim = {
  id: 327370103211001,
  eventId: 100,
  isFree: true,
  name: "ニュース7",
  startAt: 1_000,
  duration: 60 * 60 * 1000,
  networkId: 32737,
  serviceId: 1032,
  genres: [{ lv1: 0, lv2: 0, un1: 0, un2: 0 }],
};

describe("programs api client", () => {
  it("fetchSlimPrograms: window 無しは GET /api/programs の JSON を返す", async () => {
    const fetchFn = ((requestUrl: RequestInfo | URL) => {
      expect(String(requestUrl)).toBe("/api/programs");
      return Promise.resolve(Response.json([slim]));
    }) as typeof fetch;

    expect(await fetchSlimPrograms(undefined, fetchFn)).toEqual([slim]);
  });

  it("fetchSlimPrograms: from/to が揃ったら窓クエリを付ける", async () => {
    const fetchFn = ((requestUrl: RequestInfo | URL) => {
      expect(String(requestUrl)).toBe("/api/programs?from=1000&to=2000");
      return Promise.resolve(Response.json([slim]));
    }) as typeof fetch;

    expect(await fetchSlimPrograms({ from: 1000, to: 2000 }, fetchFn)).toEqual([slim]);
  });

  it("fetchSlimPrograms: from/to は片方だけでは付けない", async () => {
    const fetchFn = ((requestUrl: RequestInfo | URL) => {
      expect(String(requestUrl)).toBe("/api/programs");
      return Promise.resolve(Response.json([]));
    }) as typeof fetch;

    expect(await fetchSlimPrograms({ from: 1000 }, fetchFn)).toEqual([]);
  });

  it("fetchSlimPrograms: エラー応答は throw する", async () => {
    const fetchFn = (() => Promise.resolve(new Response("ng", { status: 500 }))) as typeof fetch;

    await expect(fetchSlimPrograms({ from: 1, to: 2 }, fetchFn)).rejects.toThrow();
  });

  it("fullProgramOf: 実応答どおり単一オブジェクトで来たらそのまま返す", () => {
    const full: MirakurunProgram = { ...slim, description: "詳細" };
    expect(fullProgramOf(full as unknown as MirakurunProgram[])).toEqual(full);
  });

  it("fullProgramOf: 型どおり配列で来たら先頭を返す", () => {
    const full: MirakurunProgram = { ...slim };
    expect(fullProgramOf([full])).toEqual(full);
    expect(fullProgramOf([])).toBeUndefined();
    expect(fullProgramOf(undefined)).toBeUndefined();
  });
});
