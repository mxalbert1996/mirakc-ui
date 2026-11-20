// Vitest stub for mpegts.js.
//
// Player.tsx dynamically imports "mpegts.js". The real package pulls in
// worker-bundling machinery that is irrelevant to tests, so the tests inject
// a pending loader into Player and never execute it; vitest.config.ts aliases
// the bare import to this local stub so module resolution still succeeds.
// The stub is never expected to be called, hence minimal.
export default {
  isSupported: () => false,
  createPlayer: () => ({}),
  Events: {},
};
