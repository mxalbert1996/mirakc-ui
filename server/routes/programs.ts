import { Hono } from "hono";
import type { SlimProgram } from "../lib/programs-cache.ts";

export type ProgramsRoutesOptions = {
  /** 番組一覧 (programs-cache の slim 形式)。失敗時は 500 で応答する。 */
  getPrograms: () => Promise<SlimProgram[]>;
};

/** epoch ms の整数表現か。空文字・小数・指数表記は弾く。 */
function parseEpochMs(value: string): number | null {
  return /^-?\d+$/.test(value) ? Number(value) : null;
}

/**
 * 番組一覧 API。`/api/programs` にマウントする。
 *
 * - GET /            キャッシュ済みの番組一覧 (slim) 全件
 * - GET /?from=&to=  from/to (epoch ms) と放送時間が重複する番組だけ
 *
 * 重複判定は client (components/organisms/Program/Table.tsx) と同じ
 * `p.startAt < to && p.startAt + p.duration > from`。from/to は両方指定するか
 * 両方省略する。getPrograms の失敗 (MIRAKC_URL 未設定や mirakc 到達不能) は
 * mirakc プロキシと同じ未設定規約の 500 JSON で応答する。
 */
export function createProgramsRoutes(options: ProgramsRoutesOptions): Hono {
  const app = new Hono();

  app.get("/", async (c) => {
    const fromRaw = c.req.query("from");
    const toRaw = c.req.query("to");
    if ((fromRaw === undefined) !== (toRaw === undefined)) {
      return c.json({ error: "from and to must be specified together" }, 400);
    }

    let window: { from: number; to: number } | null = null;
    if (fromRaw !== undefined && toRaw !== undefined) {
      const from = parseEpochMs(fromRaw);
      const to = parseEpochMs(toRaw);
      if (from === null || to === null) {
        return c.json({ error: "from and to must be integers" }, 400);
      }
      window = { from, to };
    }

    let programs: SlimProgram[];
    try {
      programs = await options.getPrograms();
    } catch (e) {
      // 応答は原因によらず mirakc プロキシと同じ未設定規約の 500 JSON だが、
      // 実際の失敗原因 (MIRAKC_URL 未設定以外もあり得る) はログに残す。
      console.error("[programs-api] getPrograms failed:", e);
      return c.json({ error: "MIRAKC_URL is not set" }, 500);
    }

    if (window === null) {
      return c.json(programs);
    }
    const { from, to } = window;
    return c.json(
      programs.filter((p) => p.startAt < to && p.startAt + p.duration > from),
    );
  });

  return app;
}
