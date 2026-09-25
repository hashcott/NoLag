package main

import (
	"image"
	"image/color"
	"math"
)

// state is what the tray icon is saying at a glance.
type state int

const (
	stateOff state = iota
	stateOn
	stateFault
)

// iconColours are the three things this can be telling somebody. Chosen to be
// distinguishable side by side at sixteen pixels on a taskbar, which rules out
// any pair separated only by brightness.
var iconColours = map[state]color.NRGBA{
	stateOff:   {0x8A, 0x8E, 0x93, 0xFF}, // grey: not connected
	stateOn:    {0x2E, 0x9E, 0x4F, 0xFF}, // green: routing through a relay
	stateFault: {0xD1, 0x43, 0x43, 0xFF}, // red: connected but something is wrong
}

// trayIcon draws the icon for one state.
//
// Drawn rather than shipped as a resource: an .ico has to be compiled into the
// binary by a tool that does not run on the machine this is built on, and the
// tray icon is the only part of this product that is visible all the time. A
// ring means idle and a filled ring means traffic is going through a relay, so
// the two are distinct in shape as well as colour — a quarter of the men who
// might use this cannot rely on the colour alone.
func trayIcon(s state, size int) image.Image {
	if size < 8 {
		size = 8
	}
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	c := iconColours[s]

	centre := float64(size-1) / 2
	outer := float64(size) * 0.46
	inner := float64(size) * 0.30 // the hole in the ring
	dot := float64(size) * 0.20   // the filled centre, when connected

	// Four samples per axis. Enough that the curve does not look like a staircase
	// at sixteen pixels, and cheap at this size.
	const sub = 4
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var covered float64
			for sy := 0; sy < sub; sy++ {
				for sx := 0; sx < sub; sx++ {
					px := float64(x) + (float64(sx)+0.5)/sub - 0.5
					py := float64(y) + (float64(sy)+0.5)/sub - 0.5
					d := math.Hypot(px-centre, py-centre)
					if d <= outer && d >= inner {
						covered++
						continue
					}
					if s == stateOn && d <= dot {
						covered++
					}
				}
			}
			if covered == 0 {
				continue
			}
			alpha := covered / (sub * sub)
			img.SetNRGBA(x, y, color.NRGBA{R: c.R, G: c.G, B: c.B, A: uint8(alpha*255 + 0.5)})
		}
	}
	return img
}
