package transcode

import (
	"slices"
	"testing"
)

// Port of server/lib/quality.test.ts.
func TestQualities(t *testing.T) {
	if want := []Quality{"480p", "720p", "1080p"}; !slices.Equal(Qualities, want) {
		t.Fatalf("Qualities = %v, want %v", Qualities, want)
	}
}

func TestIsQuality(t *testing.T) {
	for _, q := range Qualities {
		if !IsQuality(string(q)) {
			t.Errorf("IsQuality(%q) = false", q)
		}
	}
	for _, v := range []string{"9999p", ""} {
		if IsQuality(v) {
			t.Errorf("IsQuality(%q) = true", v)
		}
	}
}

func TestNormalizeQualityKeepsKnownValues(t *testing.T) {
	for _, q := range []string{"480p", "720p", "1080p"} {
		if got := NormalizeQuality(q, DefaultQuality); got != Quality(q) {
			t.Errorf("NormalizeQuality(%q) = %q, want %q", q, got, q)
		}
	}
}

func TestNormalizeQualityFallsBack(t *testing.T) {
	for _, v := range []string{"", "9999p"} {
		if got := NormalizeQuality(v, DefaultQuality); got != DefaultQuality {
			t.Errorf("NormalizeQuality(%q) = %q, want %q", v, got, DefaultQuality)
		}
	}
	if got := NormalizeQuality("9999p", Quality480p); got != Quality480p {
		t.Errorf("NormalizeQuality custom fallback = %q, want 480p", got)
	}
	if got := NormalizeQuality("720p", Quality480p); got != Quality720p {
		t.Errorf("NormalizeQuality known value ignores fallback = %q, want 720p", got)
	}
}
