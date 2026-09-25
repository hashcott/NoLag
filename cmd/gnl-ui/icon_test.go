package main

import (
	"image"
	"image/color"
	"testing"
)

func at(img image.Image, x, y int) color.NRGBA {
	c := color.NRGBAModel.Convert(img.At(x, y))
	return c.(color.NRGBA)
}

func TestTheCornersAreTransparent(t *testing.T) {
	// A tray icon with opaque corners is a square box on the taskbar.
	const size = 32
	img := trayIcon(stateOn, size)
	for _, p := range [][2]int{{0, 0}, {size - 1, 0}, {0, size - 1}, {size - 1, size - 1}} {
		if a := at(img, p[0], p[1]).A; a != 0 {
			t.Errorf("corner %v has alpha %d, want 0", p, a)
		}
	}
}

func TestConnectedAndIdleDifferInShapeNotJustColour(t *testing.T) {
	// Somebody who cannot tell the two colours apart still has to be able to see
	// whether their traffic is going through a relay.
	const size = 32
	mid := size / 2
	if a := at(trayIcon(stateOn, size), mid, mid).A; a == 0 {
		t.Error("the connected icon has a hollow centre")
	}
	if a := at(trayIcon(stateOff, size), mid, mid).A; a != 0 {
		t.Error("the idle icon has a filled centre, so it cannot be told from connected by shape")
	}
}

func TestEachStateHasItsOwnColour(t *testing.T) {
	const size = 32
	seen := map[color.NRGBA]state{}
	for _, s := range []state{stateOff, stateOn, stateFault} {
		img := trayIcon(s, size)
		// A point on the ring itself, which every state draws.
		c := at(img, size/2, 2)
		if c.A == 0 {
			t.Fatalf("state %d drew nothing on the ring", s)
		}
		c.A = 255
		if prev, dup := seen[c]; dup {
			t.Errorf("states %d and %d draw the same colour %v", prev, s, c)
		}
		seen[c] = s
	}
}

func TestATinySizeStillDraws(t *testing.T) {
	// Windows asks for small icons at low DPI; one that comes back blank leaves
	// the user with an empty slot in the tray.
	img := trayIcon(stateOn, 8)
	var lit int
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if at(img, x, y).A > 0 {
				lit++
			}
		}
	}
	if lit == 0 {
		t.Fatal("an 8-pixel icon is blank")
	}
}
