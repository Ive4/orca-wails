package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	"orca-wails/internal/tasks"
)

func TestTaskLightsReflectStatesAndReportNewAttention(t *testing.T) {
	var tracker trayTaskTracker
	initial := tasks.Summary{Rows: []tasks.Row{
		{ID: "running", Status: tasks.Working},
		{ID: "monitoring", Status: tasks.Monitoring},
		{ID: "waiting", Status: tasks.Waiting},
		{ID: "unknown", Status: tasks.Unknown},
		{ID: "done", Status: tasks.Done},
	}}
	lights, change := tracker.observe(initial)
	if lights.running != 2 || lights.waiting != 1 || lights.blocked != 0 || change.notice() != "" {
		t.Fatalf("initial state = %+v, %q", lights, change.notice())
	}

	changed := tasks.Summary{Rows: []tasks.Row{
		{ID: "running", Status: tasks.Waiting},
		{ID: "monitoring", Status: tasks.Monitoring},
		{ID: "waiting", Status: tasks.Blocked},
	}}
	lights, change = tracker.observe(changed)
	if lights != (trayTaskLights{running: 1, waiting: 1, blocked: 1}) ||
		change.notice() != "1 项任务等待输入 · 1 项任务受阻" {
		t.Fatalf("changed state = %+v, %q", lights, change.notice())
	}
	if _, change = tracker.observe(changed); change.notice() != "" {
		t.Fatalf("repeated snapshot notified again: %q", change.notice())
	}
	tracker.observe(tasks.Summary{Partial: true})
	if _, change = tracker.observe(changed); change.notice() != "" {
		t.Fatalf("partial snapshot cleared known status: %q", change.notice())
	}
	if _, change = tracker.observe(tasks.Summary{Rows: []tasks.Row{
		{ID: "new", Status: tasks.Blocked},
	}}); change.notice() != "1 项任务受阻" {
		t.Fatalf("new blocked task notification = %q", change.notice())
	}
}

func TestTaskAttentionClearsOnlyAfterConfirmedResolution(t *testing.T) {
	var tracker trayTaskTracker
	tracker.observe(tasks.Summary{Rows: []tasks.Row{{ID: "task", Status: tasks.Working}}})
	_, change := tracker.observe(tasks.Summary{Rows: []tasks.Row{{ID: "task", Status: tasks.Waiting}}})
	if len(change.attention) != 1 || change.attention[0].ID != "task" {
		t.Fatalf("missing task notification: %+v", change)
	}
	for _, summary := range []tasks.Summary{
		{Partial: true},
		{Rows: []tasks.Row{{ID: "task", Status: tasks.Unknown}}},
		{Rows: []tasks.Row{{ID: "task", Status: tasks.Waiting}}},
	} {
		_, change = tracker.observe(summary)
		if len(change.cleared) != 0 || len(change.attention) != 0 {
			t.Fatalf("unconfirmed state changed notification: %+v", change)
		}
	}
	_, change = tracker.observe(tasks.Summary{})
	if len(change.cleared) != 1 || change.cleared[0] != "task" {
		t.Fatalf("missing notification removal: %+v", change)
	}
}

func TestWindowsTrayMarksTasksNeedingAttention(t *testing.T) {
	for _, item := range []struct {
		status         trayTaskLights
		needsAttention bool
	}{
		{trayTaskLights{}, false},
		{trayTaskLights{running: 12}, false},
		{trayTaskLights{waiting: 2}, true},
		{trayTaskLights{blocked: 1}, true},
		{trayTaskLights{waiting: 2, unavailable: true}, false},
	} {
		if got := item.status.needsAttention(); got != item.needsAttention {
			t.Fatalf("attention badge for %+v = %t, want %t", item.status, got, item.needsAttention)
		}
	}
	tooltip := (trayTaskLights{running: 12, waiting: 2, blocked: 1}).tooltip()
	for _, detail := range []string{"执行与监测 12", "等待输入 2", "受阻 1"} {
		if !strings.Contains(tooltip, detail) {
			t.Fatalf("missing %q in tooltip %q", detail, tooltip)
		}
	}
}

func TestWindowsAttentionIconMarksExistingIcon(t *testing.T) {
	base, err := png.Decode(bytes.NewReader(trayColorIcon))
	if err != nil {
		t.Fatal(err)
	}
	marked, err := png.Decode(bytes.NewReader(trayAttentionIcon))
	if err != nil {
		t.Fatal(err)
	}
	if base.Bounds() != marked.Bounds() || base.At(12, 18) != marked.At(12, 18) || base.At(26, 6) == marked.At(26, 6) {
		t.Fatal("the attention mark should preserve the icon and add a single visible badge")
	}
}

func TestMacTrayApplicationMarkFillsIconWithTransparentBackground(t *testing.T) {
	icon, err := png.Decode(bytes.NewReader(drawMacTrayIcon(0, 255, false)))
	if err != nil {
		t.Fatal(err)
	}
	var mark image.Rectangle
	for y := icon.Bounds().Min.Y; y < icon.Bounds().Max.Y; y++ {
		for x := icon.Bounds().Min.X; x < icon.Bounds().Max.X; x++ {
			pixel := color.NRGBAModel.Convert(icon.At(x, y)).(color.NRGBA)
			if pixel.A > 127 {
				mark = mark.Union(image.Rect(x, y, x+1, y+1))
				if pixel.R > 210 && pixel.G > 210 && pixel.B > 210 {
					t.Fatal("icon artwork has a white fill or outline")
				}
			}
		}
	}
	if mark.Dx()*3 < icon.Bounds().Dx()*2 {
		t.Errorf("application mark is too small: %v inside %v", mark, icon.Bounds())
	}
	if _, _, _, alpha := icon.At(32, 15).RGBA(); alpha != 0 {
		t.Errorf("the space between the cat ears should be transparent, got alpha %d", alpha)
	}
	dark, err := png.Decode(bytes.NewReader(drawMacTrayIcon(0, 255, true)))
	if err != nil {
		t.Fatal(err)
	}
	for y := range icon.Bounds().Dy() {
		for x := range icon.Bounds().Dx() {
			_, _, _, lightAlpha := icon.At(x, y).RGBA()
			r, g, b, darkAlpha := dark.At(x, y).RGBA()
			if lightAlpha != darkAlpha {
				t.Fatalf("menu bar appearance changed icon transparency at (%d, %d)", x, y)
			}
			if darkAlpha == 65535 && (r < 58000 || g < 58000 || b < 58000) {
				t.Fatalf("dark menu bar glyph is too faint at (%d, %d)", x, y)
			}
		}
	}
}

func TestMacTrayRingShowsConcurrentStates(t *testing.T) {
	readIcon := func(lights trayTaskLights, opacity uint8) map[string]color.NRGBA {
		t.Helper()
		icon, err := png.Decode(bytes.NewReader(drawMacTrayIcon(lights.macState(), opacity, false)))
		if err != nil {
			t.Fatal(err)
		}
		return map[string]color.NRGBA{
			"running": color.NRGBAModel.Convert(icon.At(32, 60)).(color.NRGBA),
			"waiting": color.NRGBAModel.Convert(icon.At(56, 18)).(color.NRGBA),
			"blocked": color.NRGBAModel.Convert(icon.At(7, 18)).(color.NRGBA),
		}
	}
	idle := readIcon(trayTaskLights{}, 255)
	for state, pixel := range idle {
		if pixel.A != 0 {
			t.Errorf("idle icon has a visible %s arc", state)
		}
	}
	running := readIcon(trayTaskLights{running: 3}, 255)
	if p := running["running"]; p.B <= p.R || p.A != 255 {
		t.Errorf("running arc should be blue: %v", p)
	}
	if p := running["waiting"]; p.R != p.G || p.G != p.B {
		t.Errorf("an inactive arc should be neutral: %v", p)
	}
	all := trayTaskLights{running: 3, waiting: 2, blocked: 1, partial: true}
	bright, dim := readIcon(all, 255), readIcon(all, 170)
	if bright["running"] != running["running"] || dim["running"].A >= bright["running"].A || dim["running"].A == 0 {
		t.Errorf("the running arc should dim and stay visible: bright=%v dim=%v", bright["running"], dim["running"])
	}
	if p := bright["waiting"]; p.R <= p.G || p.G <= p.B || p != dim["waiting"] {
		t.Errorf("waiting arc should stay steadily amber: %v", p)
	}
	if p := bright["blocked"]; p.R <= p.G || p.R <= p.B || p != dim["blocked"] {
		t.Errorf("blocked arc should stay steadily red: %v", p)
	}
	var tracker trayTaskTracker
	unknown, _ := tracker.observe(tasks.Summary{Rows: []tasks.Row{{ID: "restored", Status: tasks.Unknown}}})
	for _, status := range []trayTaskLights{{unavailable: true, running: 1}, {partial: true}, unknown} {
		for state, pixel := range readIcon(status, 255) {
			if pixel.A == 0 || pixel.R != pixel.G || pixel.G != pixel.B {
				t.Errorf("unconfirmed %s arc in %+v should be visibly gray: %v", state, status, pixel)
			}
		}
	}
}

func TestMacTrayPulseRespectsReducedMotion(t *testing.T) {
	if start, mid, end := trayPulseOpacity(0, false), trayPulseOpacity(1600*time.Millisecond, false), trayPulseOpacity(3200*time.Millisecond, false); start != 255 || mid < 160 || mid >= start || end != start {
		t.Fatalf("expected the blue arc to dim gently and return to full brightness: %d, %d, %d", start, mid, end)
	}
	if got := trayPulseOpacity(1600*time.Millisecond, true); got != 255 {
		t.Fatalf("expected a steady blue arc with reduced motion: %d", got)
	}
}
