package transcode

// Quality is a transcode quality preset. The presets are the single
// source shared by the client quality menu and this package
// (server/lib/quality.ts); adding one requires a matching
// QualitySettings entry.
type Quality string

const (
	Quality480p  Quality = "480p"
	Quality720p  Quality = "720p"
	Quality1080p Quality = "1080p"

	// DefaultQuality is the fallback for unknown ?quality= values.
	DefaultQuality = Quality1080p
)

// Qualities lists every preset in ascending order.
var Qualities = []Quality{Quality480p, Quality720p, Quality1080p}

// IsQuality reports whether value is a known preset.
func IsQuality(value string) bool {
	switch Quality(value) {
	case Quality480p, Quality720p, Quality1080p:
		return true
	}
	return false
}

// NormalizeQuality rounds an unknown value to the fallback.
func NormalizeQuality(value string, fallback Quality) Quality {
	if IsQuality(value) {
		return Quality(value)
	}
	return fallback
}
