package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"runtime"
	"strings"

	"orca-wails/internal/tasks"
)

// The cat mark from 128x128.png, cropped to remove the app icon tile.
//
//go:embed build/tray-cat.png
var trayIcon []byte

//go:embed build/tray-cat-color.png
var trayColorIcon []byte

var trayAttentionIcon = markTrayIcon()

type trayTaskLights struct {
	running, waiting, blocked         int
	partial, unavailable, unconfirmed bool
}

type trayTaskTracker struct {
	statuses map[string]tasks.Status
}

type trayTaskChanges struct {
	attention []tasks.Row
	cleared   []string
}

func (change trayTaskChanges) notice() string {
	var waiting, blocked int
	for _, row := range change.attention {
		if row.Status == tasks.Waiting {
			waiting++
		} else {
			blocked++
		}
	}
	var notices []string
	if waiting > 0 {
		notices = append(notices, fmt.Sprintf("%d 项任务等待输入", waiting))
	}
	if blocked > 0 {
		notices = append(notices, fmt.Sprintf("%d 项任务受阻", blocked))
	}
	return strings.Join(notices, " · ")
}

func taskNeedsAttention(status tasks.Status) bool {
	return status == tasks.Waiting || status == tasks.Blocked
}

func (tracker *trayTaskTracker) observe(summary tasks.Summary) (trayTaskLights, trayTaskChanges) {
	previous := tracker.statuses
	next := previous
	// Keep the last known state for workspaces omitted from a partial snapshot.
	if next == nil || !summary.Partial {
		next = make(map[string]tasks.Status, len(summary.Rows))
	}
	lights := trayTaskLights{partial: summary.Partial}
	var change trayTaskChanges
	for _, row := range summary.Rows {
		before := previous[row.ID]
		status := row.Status
		switch status {
		case tasks.Working, tasks.Monitoring:
			lights.running++
		case tasks.Waiting:
			lights.waiting++
		case tasks.Blocked:
			lights.blocked++
		case tasks.Unknown:
			lights.unconfirmed = true
			if before != "" {
				status = before
			}
		}
		if taskNeedsAttention(status) && previous != nil && before != status {
			change.attention = append(change.attention, row)
		}
		if taskNeedsAttention(before) && !taskNeedsAttention(status) {
			change.cleared = append(change.cleared, row.ID)
		}
		next[row.ID] = status
	}
	if !summary.Partial {
		for id, status := range previous {
			if _, present := next[id]; !present && taskNeedsAttention(status) {
				change.cleared = append(change.cleared, id)
			}
		}
	}
	tracker.statuses = next
	return lights, change
}

func (lights trayTaskLights) tooltip() string {
	if lights.unavailable {
		return appName + " · 暂时读不到任务状态"
	}
	text := fmt.Sprintf("%s · 执行与监测 %d · 等待输入 %d · 受阻 %d", appName, lights.running, lights.waiting, lights.blocked)
	if lights.partial {
		text += " · 部分工作区"
	}
	return text
}

func (lights trayTaskLights) needsAttention() bool {
	return !lights.unavailable && (lights.waiting > 0 || lights.blocked > 0)
}

func (d *desktop) setTrayLights(lights trayTaskLights) {
	d.tray.SetTooltip(lights.tooltip())
	if runtime.GOOS == "darwin" {
		d.tray.SetIcon(drawMacTrayIcon(lights.macState(), 255, false))
		return
	}
	if !lights.needsAttention() {
		d.tray.SetIcon(trayColorIcon)
		return
	}
	d.tray.SetIcon(trayAttentionIcon)
}

func markTrayIcon() []byte {
	base, err := png.Decode(bytes.NewReader(trayColorIcon))
	if err != nil {
		return trayColorIcon
	}
	icon := image.NewNRGBA(base.Bounds())
	draw.Draw(icon, icon.Bounds(), base, base.Bounds().Min, draw.Src)
	center := image.Pt(icon.Bounds().Max.X-6, icon.Bounds().Min.Y+6)
	for dy := -5; dy <= 5; dy++ {
		for dx := -5; dx <= 5; dx++ {
			distance := dx*dx + dy*dy
			switch {
			case distance <= 16:
				icon.SetNRGBA(center.X+dx, center.Y+dy, color.NRGBA{R: 216, G: 125, B: 25, A: 255})
			case distance <= 25:
				icon.SetNRGBA(center.X+dx, center.Y+dy, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
			}
		}
	}
	var out bytes.Buffer
	_ = png.Encode(&out, icon)
	return out.Bytes()
}
