package switch__test

import (
	"testing"

	sw "github.com/mousebridge/core/internal/switch"
)

func TestEdgeDetectRight(t *testing.T) {
	result := sw.CheckEdge(sw.Position{X: 2559, Y: 720}, sw.ScreenSize{W: 2560, H: 1440})
	if result.Edge != "right" {
		t.Fatalf("want right got %q", result.Edge)
	}
	if result.Pct < 0.49 || result.Pct > 0.51 {
		t.Fatalf("pct: want ~0.5 got %f", result.Pct)
	}
}

func TestEdgeDetectLeft(t *testing.T) {
	result := sw.CheckEdge(sw.Position{X: 0, Y: 360}, sw.ScreenSize{W: 2560, H: 1440})
	if result.Edge != "left" {
		t.Fatalf("want left got %q", result.Edge)
	}
}

func TestEdgeDetectNone(t *testing.T) {
	result := sw.CheckEdge(sw.Position{X: 1280, Y: 720}, sw.ScreenSize{W: 2560, H: 1440})
	if result.Edge != "" {
		t.Fatalf("want no edge got %q", result.Edge)
	}
}

func TestEdgeDetectTop(t *testing.T) {
	result := sw.CheckEdge(sw.Position{X: 640, Y: 0}, sw.ScreenSize{W: 2560, H: 1440})
	if result.Edge != "top" {
		t.Fatalf("want top got %q", result.Edge)
	}
}

func TestEdgeDetectBottom(t *testing.T) {
	result := sw.CheckEdge(sw.Position{X: 640, Y: 1439}, sw.ScreenSize{W: 2560, H: 1440})
	if result.Edge != "bottom" {
		t.Fatalf("want bottom got %q", result.Edge)
	}
}
