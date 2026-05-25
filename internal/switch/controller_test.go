package switch__test

import (
	"testing"
	"time"

	sw "github.com/mousebridge/core/internal/switch"
)

func TestControllerDefaultTargetIsLocal(t *testing.T) {
	c := sw.NewController("local-id")
	if c.ActiveTarget() != "local-id" {
		t.Fatalf("want local-id got %q", c.ActiveTarget())
	}
}

func TestControllerSwitchTo(t *testing.T) {
	c := sw.NewController("local-id")
	c.SwitchTo("remote-id")
	if c.ActiveTarget() != "remote-id" {
		t.Fatalf("want remote-id got %q", c.ActiveTarget())
	}
}

func TestControllerSwitchBack(t *testing.T) {
	c := sw.NewController("local-id")
	c.SwitchTo("remote-id")
	c.SwitchBack()
	if c.ActiveTarget() != "local-id" {
		t.Fatalf("want local-id after switch back got %q", c.ActiveTarget())
	}
}

func TestControllerDwellPreventsFlicker(t *testing.T) {
	c := sw.NewController("local-id")
	c.SetDwell(50 * time.Millisecond)

	triggered := c.TryEdgeSwitch("remote-id", "right", 0.5)
	if triggered {
		t.Fatal("should not switch immediately, dwell not elapsed")
	}

	time.Sleep(60 * time.Millisecond)
	triggered = c.TryEdgeSwitch("remote-id", "right", 0.5)
	if !triggered {
		t.Fatal("should switch after dwell elapsed")
	}
	if c.ActiveTarget() != "remote-id" {
		t.Fatalf("want remote-id got %q", c.ActiveTarget())
	}
}
