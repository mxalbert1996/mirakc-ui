/**
 * Transcode quality presets for the watch menu and the URL query.
 *
 * Client-side contract module. The Go server keeps its own copy of the same
 * preset values (transcode API / ffmpeg settings, covered by the Go tests),
 * so this list only needs to stay aligned with the Go implementation.
 */

export const qualities = ["480p", "720p", "1080p"] as const;

export type Quality = (typeof qualities)[number];

export const defaultQuality: Quality = "1080p";

function isQuality(value: unknown): value is Quality {
  return (qualities as readonly unknown[]).includes(value);
}

/** 未知の値を fallback (既定は defaultQuality) に丸める。 */
export function normalizeQuality(value: unknown, fallback: Quality = defaultQuality): Quality {
  return isQuality(value) ? value : fallback;
}
