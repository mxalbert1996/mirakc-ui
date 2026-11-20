import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";
import svgr from "vite-plugin-svgr";
import { tanstackRouter } from "@tanstack/router-plugin/vite";

// Vite does not populate process.env from .env; the dev proxy and allowed
// hosts must be resolved through loadEnv so the root .env (see .env.example)
// actually applies. Real environment variables always win.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, ".", "");
  const allowedHostsEnv = env.VITE_ALLOWED_HOSTS?.trim();
  const allowedHosts: true | string[] | undefined = allowedHostsEnv
    ? allowedHostsEnv === "true" || allowedHostsEnv === "all"
      ? true
      : allowedHostsEnv
          .split(",")
          .map((h) => h.trim())
          .filter(Boolean)
    : undefined;

  // Port the Go dev server (pnpm dev:server) listens on. The client and the
  // API server run as separate processes during development, and the Vite dev
  // server proxies /api/* there. In production the single Go binary serves
  // both the embedded client/dist and /api, so this proxy is dev-only.
  const apiPort = env.PORT ?? "8000";

  return {
    root: "client",
    // SPA の多階層ルート (/watch/$serviceId 等) でも asset を解決できるよう絶対パス
    // にする。"./" (相対) だと 2 階層パスから ./assets が /watch/assets に解決され
    // 404 -> SPA フォールバックの index.html (text/html) を JS として読み MIME エラーになる。
    base: "/",
    // 依存最適化キャッシュを root(client) 直下ではなくルートの node_modules 配下に
    // 逃がす。client/ は devcontainer で bind mount されており、root(client) 直下だと
    // コンテナ(root)生成の .vite がホスト(非 root)の vite と衝突して EACCES で落ちる。
    // node_modules は devcontainer では named volume なのでコンテナ/ホストで分離される。
    cacheDir: "../node_modules/.vite",
    build: {
      outDir: "dist",
      emptyOutDir: true,
    },
    server: {
      // devcontainer / リモートホスト経由のアクセスを許可する (旧 compose の
      // --host 0.0.0.0 相当)。
      host: true,
      allowedHosts,
      proxy: {
        "/api": {
          target: `http://localhost:${apiPort}`,
          changeOrigin: true,
        },
      },
    },
    plugins: [
      // svgr: `*.svg?react` を React コンポーネントとして取り込む。インライン SVG を
      // 禁止し assets/images へ外出しするため、currentColor 追従が要る意匠で使う。
      svgr(),
      // tanstackRouter は react() より前に置く必要がある。
      // root を "client" にしているため、各パスは root(client) 基準で解決される。
      // Deno は import に拡張子を要求するため、生成される routeTree.gen.ts の
      // import にも .tsx を付与する。Node/Vite では必須ではないが、拡張付きのまま
      // でも問題なく動作するため従来の生成物と同じ形を保つ。
      tanstackRouter({
        target: "react",
        autoCodeSplitting: true,
        routesDirectory: "./routes",
        generatedRouteTree: "./routeTree.gen.ts",
        addExtensions: true,
      }),
      react(),
    ],
  };
});
