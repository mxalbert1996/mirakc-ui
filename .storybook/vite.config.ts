import { defineConfig } from "vite";
import svgr from "vite-plugin-svgr";

// Storybook-only minimal Vite config.
//
// The project's vite.config.ts carries the tanstackRouter plugin (routeTree
// generation), root:"client" and server.proxy — inheriting it as-is breaks
// Storybook with ENOENT (missing ./routes), root mismatches and a double
// @vitejs/plugin-react registration. .storybook/main.ts points
// builder.viteConfigPath at this file so the project config's auto-discovery
// and merging are cut off. All package imports resolve from node_modules via
// plain npm resolution; the react plugin is added by @storybook/react-vite
// itself, so it is not listed here.
export default defineConfig({
  // svgr: load `*.svg?react` as React components, same as vite.config.ts.
  plugins: [svgr()],
});
