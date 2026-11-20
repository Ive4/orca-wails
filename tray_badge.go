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
	"strconv"
)

//go:embed build/tray-cat.png
var trayIcon []byte

//go:embed build/tray-cat-color.png
var trayColorIcon []byte

func (d *desktop) setTrayBadge(count int) {
	tooltip, label := appName, ""
	if count > 0 {
		label = strconv.Itoa(count)
		tooltip = fmt.Sprintf("Orca · %d 项待处理", count)
	}
	d.tray.SetTooltip(tooltip)
	if runtime.GOOS == "darwin" {
		d.tray.SetLabel(label)
	} else {
		d.tray.SetIcon(trayBadgeIcon(count))
	}
}

func trayBadgeIcon(count int) []byte {
	if count <= 0 {
		return trayColorIcon
	}
	base, err := png.Decode(bytes.NewReader(trayColorIcon))
	if err != nil {
		return trayColorIcon
	}
	icon := image.NewNRGBA(base.Bounds())
	draw.Draw(icon, icon.Bounds(), base, base.Bounds().Min, draw.Src)
	badge := color.NRGBA{R: 250, G: 218, B: 157, A: 255}
	ink := color.NRGBA{R: 85, G: 50, B: 10, A: 255}
	for y := 0; y < 13; y++ {
		for x := 19; x < 32; x++ {
			dx, dy := x-25, y-6
			if dx*dx+dy*dy <= 40 {
				icon.SetNRGBA(x, y, badge)
			}
		}
	}
	digits := [...][5]uint8{
		{2, 6, 2, 2, 7}, {7, 1, 7, 4, 7}, {7, 1, 7, 1, 7},
		{5, 5, 7, 1, 1}, {7, 4, 7, 1, 7}, {7, 4, 7, 5, 7},
		{7, 1, 2, 2, 2}, {7, 5, 7, 5, 7}, {7, 5, 7, 1, 7},
	}
	left := 24
	if count > 9 {
		left = 22
		for _, point := range []image.Point{{X: 28, Y: 5}, {X: 27, Y: 6}, {X: 28, Y: 6}, {X: 29, Y: 6}, {X: 28, Y: 7}} {
			icon.SetNRGBA(point.X, point.Y, ink)
		}
	}
	for y, row := range digits[min(count, 9)-1] {
		for x := 0; x < 3; x++ {
			if row&(4>>x) != 0 {
				icon.SetNRGBA(left+x, y+4, ink)
			}
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, icon); err != nil {
		return trayColorIcon
	}
	return out.Bytes()
}
