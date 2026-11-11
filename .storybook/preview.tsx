import { useLayoutEffect, type ReactNode } from "react";
import type { Preview } from "@storybook/react-vite";

// SPA と同じグローバル CSS (CSS 変数 + light-dark トークン)。
import "../client/assets/styles/palette.css";
import "../client/assets/styles/general.css";
import "../client/assets/styles/layout.css";

// 手動テーマは html[data-theme] で切り替える。lightningcss は light-dark() を
// downlevel する際、color-scheme を宣言するセレクタにしか変数上書きを生成しない。
// Storybook のビルドも Vite の既定ターゲットで圧縮されるため、ラッパー要素への
// color-scheme 指定では本番ビルドで効かない。
function ThemeScope({
  theme,
  children,
}: {
  theme: string;
  children: ReactNode;
}) {
  useLayoutEffect(() => {
    document.documentElement.dataset.theme = theme;
  }, [theme]);

  // モバイル分岐は palette.css の @media が立てる --is-mobile フラグ +
  // コンテナスタイルクエリで効くため、ここでのコンテナ指定は不要。
  // (SB iframe の幅に応じて :root の @media が切り替わる。)
  return (
    <div
      style={{
        background: "var(--color-bg)",
        color: "var(--color-text)",
        minHeight: "100vh",
        padding: "1.6rem",
      }}
    >
      {children}
    </div>
  );
}

const preview: Preview = {
  parameters: {
    controls: {
      matchers: {
        color: /(background|color)$/i,
        date: /Date$/i,
      },
    },
    layout: "fullscreen",
  },
  globalTypes: {
    theme: {
      description: "配色テーマ",
      defaultValue: "light",
      toolbar: {
        title: "テーマ",
        icon: "mirror",
        items: [
          { value: "light", title: "Light" },
          { value: "dark", title: "Dark" },
        ],
        dynamicTitle: true,
      },
    },
  },
  decorators: [
    (Story, context) => (
      <ThemeScope theme={(context.globals.theme as string) ?? "light"}>
        <Story />
      </ThemeScope>
    ),
  ],
};

export default preview;
