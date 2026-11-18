package switch_

import (
	"sync"
	"time"
)

const defaultDwell = 50 * time.Millisecond

// SwitchEvent is emitted when the active target changes.
type SwitchEvent struct {
	PreviousTargetID string
	TargetID         string
	Trigger          string // "edge"|"hotkey"
	Edge             string
	EntryPct         float64
}

// Controller manages which device currently receives input.
type Controller struct {
	mu           sync.Mutex
	localID      string
	activeTarget string
	dwell        time.Duration
	edgeFirstAt  time.Time
	edgePending  string
	OnSwitch     func(SwitchEvent)
}

func NewController(localID string) *Controller {
	return &Controller{
		localID:      localID,
		activeTarget: localID,
		dwell:        defaultDwell,
	}
}

func (c *Controller) SetDwell(d time.Duration) {
	c.mu.Lock()
	c.dwell = d
	c.mu.Unlock()
}

func (c *Controller) ActiveTarget() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.activeTarget
}

// SwitchTo immediately sets the active target.
func (c *Controller) SwitchTo(deviceID string) {
	c.mu.Lock()
	prev := c.activeTarget
	c.activeTarget = deviceID
	cb := c.OnSwitch
	c.mu.Unlock()
	if prev != deviceID && cb != nil {
		cb(SwitchEvent{PreviousTargetID: prev, TargetID: deviceID, Trigger: "hotkey"})
	}
}

// SwitchBack returns control to the local device.
func (c *Controller) SwitchBack() {
	c.SwitchTo(c.localID)
}

// TryEdgeSwitch records an edge hit. Returns true and switches if dwell elapsed.
func (c *Controller) TryEdgeSwitch(deviceID, edge string, entryPct float64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.edgeReadyLocked(deviceID) {
		return false
	}
	prev := c.activeTarget
	c.activeTarget = deviceID
	cb := c.OnSwitch
	if cb != nil {
		go cb(SwitchEvent{PreviousTargetID: prev, TargetID: deviceID, Trigger: "edge", Edge: edge, EntryPct: entryPct})
	}
	return true
}

// EdgeReady tracks dwell without changing the active target. Callers that need
// remote approval can wait for a grant before committing the target.
func (c *Controller) EdgeReady(deviceID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.edgeReadyLocked(deviceID)
}

func (c *Controller) edgeReadyLocked(deviceID string) bool {
	now := time.Now()
	if c.edgePending != deviceID {
		c.edgePending = deviceID
		c.edgeFirstAt = now
		return false
	}
	if now.Sub(c.edgeFirstAt) < c.dwell {
		return false
	}
	c.edgePending = ""
	return true
}
