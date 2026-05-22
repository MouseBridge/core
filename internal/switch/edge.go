package switch_

// Position is a mouse coordinate in screen pixels.
type Position struct {
	X, Y float64
}

// ScreenSize holds the display dimensions.
type ScreenSize struct {
	W, H int
}

// EdgeResult describes which edge was hit and where (0.0–1.0 along that edge).
type EdgeResult struct {
	Edge string  // "left"|"right"|"top"|"bottom"|""
	Pct  float64 // position along the edge, 0.0 = start, 1.0 = end
}

const edgeThreshold = 2.0

// CheckEdge returns which screen edge pos is touching, if any.
func CheckEdge(pos Position, screen ScreenSize) EdgeResult {
	w := float64(screen.W)
	h := float64(screen.H)

	switch {
	case pos.X <= edgeThreshold:
		return EdgeResult{Edge: "left", Pct: pos.Y / h}
	case pos.X >= w-1-edgeThreshold:
		return EdgeResult{Edge: "right", Pct: pos.Y / h}
	case pos.Y <= edgeThreshold:
		return EdgeResult{Edge: "top", Pct: pos.X / w}
	case pos.Y >= h-1-edgeThreshold:
		return EdgeResult{Edge: "bottom", Pct: pos.X / w}
	}
	return EdgeResult{}
}
