package main

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"time"
)

type macTrayState uint8

const (
	macTrayRunning macTrayState = 1 << iota
	macTrayWaiting
	macTrayBlocked
	macTrayUnconfirmed
)

const (
	trayIconSize   = 64
	trayPulseCycle = 3200 * time.Millisecond
	trayFrameDelay = 100 * time.Millisecond
)

var macTrayCats = map[bool]*image.NRGBA{
	false: drawMacTrayCat(color.NRGBA{R: 40, G: 43, B: 48, A: 255}),
	true:  drawMacTrayCat(color.NRGBA{R: 237, G: 242, B: 247, A: 255}),
}

func (lights trayTaskLights) macState() macTrayState {
	if lights.unavailable {
		return macTrayUnconfirmed
	}
	var state macTrayState
	if lights.running > 0 {
		state |= macTrayRunning
	}
	if lights.waiting > 0 {
		state |= macTrayWaiting
	}
	if lights.blocked > 0 {
		state |= macTrayBlocked
	}
	if state == 0 && (lights.partial || lights.unconfirmed) {
		return macTrayUnconfirmed
	}
	return state
}

func drawMacTrayCat(foreground color.NRGBA) *image.NRGBA {
	icon := image.NewNRGBA(image.Rect(0, 0, trayIconSize, trayIconSize))
	mask, err := png.Decode(bytes.NewReader(trayIcon))
	if err != nil {
		return icon
	}
	catRect := mask.Bounds().Sub(mask.Bounds().Min)
	catRect = catRect.Add(image.Pt((trayIconSize-catRect.Dx())/2, (trayIconSize-catRect.Dy())/2))
	draw.DrawMask(icon, catRect, image.NewUniform(foreground), image.Point{}, mask, mask.Bounds().Min, draw.Over)
	return icon
}

func drawMacTrayIcon(state macTrayState, runningOpacity uint8, darkBar bool) []byte {
	cat := macTrayCats[darkBar]
	icon := image.NewNRGBA(cat.Bounds())
	copy(icon.Pix, cat.Pix)
	if state != 0 {
		arcs := [...]struct {
			state       macTrayState
			angle       float64
			light, dark color.NRGBA
		}{
			{macTrayRunning, math.Pi / 2, color.NRGBA{R: 24, G: 98, B: 201, A: runningOpacity}, color.NRGBA{R: 156, G: 219, B: 255, A: runningOpacity}},
			{macTrayWaiting, -math.Pi / 6, color.NRGBA{R: 137, G: 81, B: 12, A: 255}, color.NRGBA{R: 255, G: 204, B: 112, A: 255}},
			{macTrayBlocked, -5 * math.Pi / 6, color.NRGBA{R: 184, G: 39, B: 65, A: 255}, color.NRGBA{R: 255, G: 177, B: 184, A: 255}},
		}
		neutral := uint8(40)
		if darkBar {
			neutral = 237
		}
		for _, arc := range arcs {
			ink := arc.light
			if darkBar {
				ink = arc.dark
			}
			switch {
			case state == macTrayUnconfirmed:
				ink = color.NRGBA{R: neutral, G: neutral, B: neutral, A: 178}
			case state&arc.state == 0:
				ink = color.NRGBA{R: neutral, G: neutral, B: neutral, A: 48}
			}
			drawTrayArc(icon, arc.angle, ink)
		}
	}
	var out bytes.Buffer
	_ = png.Encode(&out, icon)
	return out.Bytes()
}

func drawTrayArc(icon *image.NRGBA, angle float64, ink color.NRGBA) {
	const center = trayIconSize / 2
	const radius, halfWidth = 28.0, 2.6
	const halfSweep = 49 * math.Pi / 180
	for y := range trayIconSize {
		for x := range trayIconSize {
			dx, dy := float64(x)+0.5-center, float64(y)+0.5-center
			distance := math.Abs(math.Hypot(dx, dy) - radius)
			if distance >= halfWidth+0.5 {
				continue
			}
			// Measure to the endpoint outside the sweep to round each cap.
			delta := math.Remainder(math.Atan2(dy, dx)-angle, 2*math.Pi)
			if math.Abs(delta) > halfSweep {
				end := angle + math.Copysign(halfSweep, delta)
				distance = math.Hypot(dx-radius*math.Cos(end), dy-radius*math.Sin(end))
			}
			coverage := min(1, halfWidth+0.5-distance)
			if coverage > 0 {
				pixel := ink
				pixel.A = uint8(math.Round(float64(ink.A) * coverage))
				icon.SetNRGBA(x, y, pixel)
			}
		}
	}
}

func trayPulseOpacity(elapsed time.Duration, reducedMotion bool) uint8 {
	if reducedMotion {
		return 255
	}
	phase := 2 * math.Pi * float64(elapsed%trayPulseCycle) / float64(trayPulseCycle)
	return uint8(math.Round(255 - 18*(1-math.Cos(phase))))
}

type macTrayDisplay struct {
	dark, reduceMotion bool
}

func (d *desktop) animateMacTray(updates <-chan trayTaskLights) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	type frame struct {
		state   macTrayState
		opacity uint8
		darkBar bool
	}
	icons := make(map[frame][]byte)
	last := frame{opacity: 255}
	var state macTrayState
	var started, nextDisplayCheck time.Time
	var display macTrayDisplay
	for {
		select {
		case <-d.ctx.Done():
			return
		case lights := <-updates:
			state = lights.macState()
			d.tray.SetTooltip(lights.tooltip())
		case <-timer.C:
		}
		now := time.Now()
		if !now.Before(nextDisplayCheck) {
			preferences := readMacTrayDisplay()
			if preferences.reduceMotion != display.reduceMotion {
				started = now
			}
			display = preferences
			nextDisplayCheck = now.Add(time.Second)
		}
		next := frame{state: state, opacity: 255, darkBar: display.dark}
		delay := time.Second
		if state&macTrayRunning != 0 && !display.reduceMotion {
			if started.IsZero() {
				started = now
			}
			next.opacity = trayPulseOpacity(now.Sub(started), false)
			delay = trayFrameDelay
		} else {
			started = time.Time{}
		}
		timer.Reset(delay)
		if next != last {
			if _, ok := icons[next]; !ok {
				icons[next] = drawMacTrayIcon(next.state, next.opacity, next.darkBar)
			}
			d.tray.SetIcon(icons[next])
			last = next
		}
	}
}
