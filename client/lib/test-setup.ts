import { Temporal as TemporalPolyfill } from "temporal-polyfill";
import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

// The client uses the Temporal API, which the target browsers ship natively
// (as the Deno runtime did for the old dev tooling). Node 26 does not expose
// it yet, so register the spec polyfill for the happy-dom test environment
// only — the production bundle is unaffected.
if (typeof globalThis.Temporal === "undefined") {
  Object.assign(globalThis, { Temporal: TemporalPolyfill });
}

// 各テスト後に React のマウントツリーを破棄する。happy-dom 環境は
// vitest.config.ts の test.environment で供給されるため、ここでの登録は不要。
afterEach(() => {
  cleanup();
});
