//go:build windows

package main

import (
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/win"

	"gamenolag/internal/client/ipc"
)

// Sizes at 96 dpi; each is scaled by the DPI of the monitor the window is on.
const (
	miniW, miniH = 320, 236
	fullW, fullH = 1040, 656
	// miniMargin is the gap between the mini panel and the edge of the work area.
	miniMargin = 12
	// sparkBars is how many polls the mini panel's sparkline shows: 36 seconds.
	sparkBars = 12

	spiGetWorkArea = 0x0030
)

var (
	cBackground = walk.RGB(0x08, 0x0B, 0x10)
	cPanel      = walk.RGB(0x05, 0x07, 0x0A)
	cRule       = walk.RGB(0x16, 0x20, 0x2B)
	cText       = walk.RGB(0xC9, 0xD4, 0xE0)
	cBright     = walk.RGB(0xE8, 0xEE, 0xF5)
	cDim        = walk.RGB(0x6E, 0x7C, 0x8C)
	cWarn       = walk.RGB(0xF2, 0xB8, 0x4B)
)

func colour(c rgb) walk.Color { return walk.RGB(c.R, c.G, c.B) }

func levelColour(l level) walk.Color {
	switch l {
	case levelErr:
		return colour(accentFault)
	case levelWarn:
		return cWarn
	}
	return cText
}

// panels is the mini panel and the full window. Both draw what ui already
// holds — the view, the history and the event log — and neither asks the
// service anything.
type panels struct {
	u *ui

	mini, full         *walk.MainWindow
	miniHead, fullHead *walk.CustomWidget
	miniBody, fullBody *walk.CustomWidget
	miniAct, fullAct   *walk.PushButton
	fullReload         *walk.PushButton

	font, fontBig, fontHuge *walk.Font
	bg                      *walk.SolidColorBrush
	marks                   map[state]*walk.Bitmap
}

func newPanels(u *ui) (*panels, error) {
	w := &panels{u: u, marks: map[state]*walk.Bitmap{}}
	var err error
	if w.font, err = walk.NewFont("Consolas", 9, 0); err != nil {
		return nil, err
	}
	if w.fontBig, err = walk.NewFont("Consolas", 16, walk.FontBold); err != nil {
		return nil, err
	}
	if w.fontHuge, err = walk.NewFont("Consolas", 36, walk.FontBold); err != nil {
		return nil, err
	}
	if w.bg, err = walk.NewSolidColorBrush(cBackground); err != nil {
		return nil, err
	}
	// The same mark as the tray, so the window and the icon say the state in the
	// same shape.
	for _, s := range []state{stateOff, stateOn, stateFault} {
		if w.marks[s], err = walk.NewBitmapFromImageForDPI(trayIcon(s, 64), 96); err != nil {
			return nil, err
		}
	}
	if err := w.buildMini(); err != nil {
		return nil, err
	}
	if err := w.buildFull(); err != nil {
		return nil, err
	}
	w.refresh()
	return w, nil
}

func (w *panels) buildMini() error {
	mw, err := w.form(miniW, miniH)
	if err != nil {
		return err
	}
	w.mini = mw
	top, err := w.row(mw)
	if err != nil {
		return err
	}
	if w.miniHead, err = w.canvas(top, w.paintMiniHead); err != nil {
		return err
	}
	fixHeight(w.miniHead, 28)
	if _, err := w.smallButton(top, "+", w.showFull); err != nil {
		return err
	}
	if _, err := w.smallButton(top, "×", mw.Hide); err != nil {
		return err
	}
	if w.miniBody, err = w.canvas(mw, w.paintMiniBody); err != nil {
		return err
	}
	if w.miniAct, err = w.button(mw, "Connect", w.act); err != nil {
		return err
	}
	borderless(mw)
	return nil
}

func (w *panels) buildFull() error {
	mw, err := w.form(fullW, fullH)
	if err != nil {
		return err
	}
	w.full = mw
	_ = mw.SetTitle("GameNoLag")
	top, err := w.row(mw)
	if err != nil {
		return err
	}
	if w.fullHead, err = w.canvas(top, w.paintFullHead); err != nil {
		return err
	}
	fixHeight(w.fullHead, 28)
	if _, err := w.smallButton(top, "−", w.collapse); err != nil {
		return err
	}
	if w.fullBody, err = w.canvas(mw, w.paintFullBody); err != nil {
		return err
	}
	bottom, err := w.row(mw)
	if err != nil {
		return err
	}
	if w.fullAct, err = w.button(bottom, "Connect", w.act); err != nil {
		return err
	}
	if w.fullReload, err = w.button(bottom, "Refresh game list", func() {
		w.u.send(ipc.VerbReloadProfile, "Refreshing…")
	}); err != nil {
		return err
	}
	if _, err := walk.NewHSpacer(bottom); err != nil {
		return err
	}
	return nil
}

// form is a window with the dark background and a vertical layout.
//
// Closing it hides it. Destroying it would lose nothing, but the next "Open
// window" would then have to rebuild it, and closing must never be mistaken for
// disconnecting: the tunnel is the service's, and it stays up.
func (w *panels) form(width, height int) (*walk.MainWindow, error) {
	mw, err := walk.NewMainWindow()
	if err != nil {
		return nil, err
	}
	mw.SetBackground(w.bg)
	mw.SetFont(w.font)
	l := walk.NewVBoxLayout()
	_ = l.SetMargins(walk.Margins{HNear: 8, VNear: 8, HFar: 8, VFar: 8})
	_ = l.SetSpacing(6)
	if err := mw.SetLayout(l); err != nil {
		return nil, err
	}
	if err := mw.SetSize(walk.Size{Width: width, Height: height}); err != nil {
		return nil, err
	}
	mw.Closing().Attach(func(canceled *bool, _ walk.CloseReason) {
		*canceled = true
		mw.Hide()
	})
	return mw, nil
}

func (w *panels) row(parent walk.Container) (*walk.Composite, error) {
	c, err := walk.NewComposite(parent)
	if err != nil {
		return nil, err
	}
	c.SetBackground(w.bg)
	l := walk.NewHBoxLayout()
	_ = l.SetMargins(walk.Margins{})
	_ = l.SetSpacing(6)
	return c, c.SetLayout(l)
}

func (w *panels) button(parent walk.Container, text string, onClick func()) (*walk.PushButton, error) {
	b, err := walk.NewPushButton(parent)
	if err != nil {
		return nil, err
	}
	_ = b.SetText(text)
	_ = b.SetMinMaxSize(walk.Size{Height: 32}, walk.Size{})
	b.Clicked().Attach(onClick)
	return b, nil
}

func (w *panels) smallButton(parent walk.Container, text string, onClick func()) (*walk.PushButton, error) {
	b, err := w.button(parent, text, onClick)
	if err != nil {
		return nil, err
	}
	return b, b.SetMinMaxSize(walk.Size{Width: 32, Height: 28}, walk.Size{Width: 32, Height: 28})
}

// canvas is a custom-drawn area. paint gets its bounds in pixels and a function
// scaling a 96-dpi length to them.
func (w *panels) canvas(parent walk.Container, paint func(p *painter)) (*walk.CustomWidget, error) {
	var cw *walk.CustomWidget
	cw, err := walk.NewCustomWidgetPixels(parent, 0, func(c *walk.Canvas, _ walk.Rectangle) error {
		p := &painter{c: c, dpi: cw.DPI(), b: cw.ClientBoundsPixels()}
		p.fill(p.b, cBackground)
		paint(p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	cw.SetPaintMode(walk.PaintNoErase)
	cw.SetInvalidatesOnResize(true)
	return cw, nil
}

// head canvases are one line tall.
func fixHeight(cw *walk.CustomWidget, h int) {
	_ = cw.SetMinMaxSize(walk.Size{Height: h}, walk.Size{Height: h})
}

// borderless turns the mini panel into a topmost tool window: no title bar, no
// taskbar button, above other windows but not above a fullscreen game.
func borderless(mw *walk.MainWindow) {
	h := mw.Handle()
	style := uint32(win.WS_POPUP | win.WS_BORDER)
	ex := uint32(win.WS_EX_TOOLWINDOW | win.WS_EX_TOPMOST | win.WS_EX_CONTROLPARENT)
	win.SetWindowLong(h, win.GWL_STYLE, int32(style))
	win.SetWindowLong(h, win.GWL_EXSTYLE, int32(ex))
	win.SetWindowPos(h, win.HWND_TOPMOST, 0, 0, 0, 0,
		win.SWP_NOMOVE|win.SWP_NOSIZE|win.SWP_FRAMECHANGED|win.SWP_NOACTIVATE)
}

// workArea is the primary monitor's desktop minus the taskbar, wherever the
// taskbar is docked.
func workArea() win.RECT {
	var r win.RECT
	if win.SystemParametersInfo(spiGetWorkArea, 0, unsafe.Pointer(&r), 0) {
		return r
	}
	return win.RECT{Right: win.GetSystemMetrics(win.SM_CXSCREEN), Bottom: win.GetSystemMetrics(win.SM_CYSCREEN)}
}

func (w *panels) toggleMini() {
	if w.mini.Visible() {
		w.mini.Hide()
		return
	}
	w.showMini()
}

func (w *panels) showMini() {
	dpi := w.mini.DPI()
	width, height := walk.IntFrom96DPI(miniW, dpi), walk.IntFrom96DPI(miniH, dpi)
	m := walk.IntFrom96DPI(miniMargin, dpi)
	r := workArea()
	_ = w.mini.SetBoundsPixels(walk.Rectangle{
		X: int(r.Right) - width - m, Y: int(r.Bottom) - height - m,
		Width: width, Height: height,
	})
	w.mini.Show()
	win.SetWindowPos(w.mini.Handle(), win.HWND_TOPMOST, 0, 0, 0, 0, win.SWP_NOMOVE|win.SWP_NOSIZE)
	w.refresh()
}

func (w *panels) showFull() {
	w.mini.Hide()
	w.full.Show()
	_ = w.full.Activate()
	w.refresh()
}

func (w *panels) collapse() {
	w.full.Hide()
	w.showMini()
}

func (w *panels) act() {
	v := w.u.view
	pending := "Connecting…"
	if v.verb == ipc.VerbDisconnect {
		pending = "Disconnecting…"
	}
	w.u.send(v.verb, pending)
}

// refresh brings buttons and drawings up to date with ui. Called on the GUI
// thread after every poll and every verb.
func (w *panels) refresh() {
	v := w.u.view
	enabled := v.canAct && !w.u.busy
	for _, b := range []*walk.PushButton{w.miniAct, w.fullAct} {
		_ = b.SetText(v.button)
		b.SetEnabled(enabled)
	}
	w.fullReload.SetEnabled(enabled)
	if w.mini.Visible() {
		_ = w.miniHead.Invalidate()
		_ = w.miniBody.Invalidate()
	}
	if w.full.Visible() {
		_ = w.fullHead.Invalidate()
		_ = w.fullBody.Invalidate()
	}
}

func (w *panels) paintMiniHead(p *painter) { w.paintHead(p, "gnl") }
func (w *panels) paintFullHead(p *painter) { w.paintHead(p, "gamenolag ~/status") }

func (w *panels) paintHead(p *painter, title string) {
	v := w.u.view
	size := p.s(16)
	y := p.b.Y + (p.b.Height-size)/2
	_ = p.c.DrawImageStretchedPixels(w.marks[v.state], walk.Rectangle{X: p.b.X, Y: y, Width: size, Height: size})
	x := p.b.X + size + p.s(8)
	p.text(title, w.font, cBright, walk.Rectangle{X: x, Y: p.b.Y, Width: p.b.Width - x, Height: p.b.Height}, walk.TextLeft)
	p.text(v.tag, w.font, colour(v.accent), walk.Rectangle{X: x, Y: p.b.Y, Width: p.b.Width - x - p.s(4), Height: p.b.Height}, walk.TextRight)
}

func (w *panels) paintMiniBody(p *painter) {
	v := w.u.view
	x, y, wd := p.b.X, p.b.Y, p.b.Width
	p.text("rtt", w.font, cDim, walk.Rectangle{X: x, Y: y, Width: p.s(40), Height: p.s(34)}, walk.TextLeft)
	p.text(v.rtt+" ms", w.fontBig, rttColour(v), walk.Rectangle{X: x + p.s(40), Y: y, Width: p.s(140), Height: p.s(34)}, walk.TextLeft)
	w.paintSpark(p, walk.Rectangle{X: x + wd - p.s(96), Y: y + p.s(8), Width: p.s(96), Height: p.s(20)}, colour(v.accent))
	y += p.s(40)
	for _, kv := range [][2]string{{"loss", v.loss}, {"relay", v.relay}, {"game", v.game}} {
		p.text(kv[0], w.font, cDim, walk.Rectangle{X: x, Y: y, Width: p.s(60), Height: p.s(20)}, walk.TextLeft)
		p.text(kv[1], w.font, cText, walk.Rectangle{X: x + p.s(60), Y: y, Width: wd - p.s(60), Height: p.s(20)}, walk.TextRight)
		y += p.s(20)
	}
	if line := problemLine(v); line != "" {
		p.text(line, w.font, colour(accentFault), walk.Rectangle{X: x, Y: y + p.s(4), Width: wd, Height: p.s(20)}, walk.TextLeft)
	}
}

func (w *panels) paintFullBody(p *painter) {
	v := w.u.view
	gap := p.s(14)
	x, y, wd, ht := p.b.X, p.b.Y, p.b.Width, p.b.Height
	left := wd*2/3 - gap/2
	right := x + left + gap
	rightW := wd - left - gap

	// RTT and its chart.
	p.text("TUNNEL RTT · 3 min", w.font, cDim, walk.Rectangle{X: x, Y: y, Width: left, Height: p.s(18)}, walk.TextLeft)
	p.text(v.rtt+" ms", w.fontHuge, rttColour(v), walk.Rectangle{X: x, Y: y + p.s(18), Width: left, Height: p.s(64)}, walk.TextLeft)
	chart := walk.Rectangle{X: x, Y: y + p.s(90), Width: left, Height: p.s(200)}
	w.paintChart(p, chart, colour(v.accent))

	// Tiles.
	ty := y
	for _, kv := range [][2]string{{"LOSS", v.loss}, {"ROUTES", v.routes}, {"RELAY", v.relay}, {"GAME", v.game}} {
		tile := walk.Rectangle{X: right, Y: ty, Width: rightW, Height: p.s(62)}
		p.fill(tile, cPanel)
		p.frame(tile)
		in := p.inset(tile, p.s(12))
		p.text(kv[0], w.font, cDim, walk.Rectangle{X: in.X, Y: in.Y, Width: in.Width, Height: p.s(16)}, walk.TextLeft)
		p.text(kv[1], w.fontBig, cBright, walk.Rectangle{X: in.X, Y: in.Y + p.s(16), Width: in.Width, Height: p.s(24)}, walk.TextLeft)
		ty += p.s(62) + p.s(10)
	}

	// Event log, with the one-line summary or the fault above it.
	ey := chart.Y + chart.Height + gap
	status, colourOf := v.line, cDim
	if line := problemLine(v); line != "" {
		status, colourOf = line, colour(accentFault)
	}
	p.text(status, w.font, colourOf, walk.Rectangle{X: x, Y: ey, Width: wd, Height: p.s(18)}, walk.TextLeft)
	ey += p.s(22)
	panel := walk.Rectangle{X: x, Y: ey, Width: wd, Height: y + ht - ey}
	if panel.Height < p.s(40) {
		return
	}
	p.fill(panel, cPanel)
	p.frame(panel)
	in := p.inset(panel, p.s(10))
	p.text("EVENTS", w.font, cDim, walk.Rectangle{X: in.X, Y: in.Y, Width: in.Width, Height: p.s(16)}, walk.TextLeft)
	line := p.s(18)
	rows := (in.Height - p.s(20)) / line
	ly := in.Y + p.s(20)
	for _, e := range w.u.log.recent(rows) {
		p.text(e.at.Format("15:04:05"), w.font, cDim, walk.Rectangle{X: in.X, Y: ly, Width: p.s(80), Height: line}, walk.TextLeft)
		p.text(e.text, w.font, levelColour(e.level), walk.Rectangle{X: in.X + p.s(84), Y: ly, Width: in.Width - p.s(84), Height: line}, walk.TextLeft)
		ly += line
	}
}

func (w *panels) paintChart(p *painter, r walk.Rectangle, accent walk.Color) {
	p.fill(r, cPanel)
	p.frame(r)
	in := p.inset(r, p.s(10))
	if rule, err := walk.NewCosmeticPen(walk.PenSolid, cRule); err == nil {
		for i := 1; i < 4; i++ {
			gy := in.Y + in.Height*i/4
			_ = p.c.DrawLinePixels(rule, walk.Point{X: in.X, Y: gy}, walk.Point{X: in.X + in.Width, Y: gy})
		}
		rule.Dispose()
	}
	brush, err := walk.NewSolidColorBrush(accent)
	if err != nil {
		return
	}
	defer brush.Dispose()
	pen, err := walk.NewGeometricPen(walk.PenSolid|walk.PenJoinRound|walk.PenCapFlat, p.s(2), brush)
	if err != nil {
		return
	}
	defer pen.Dispose()
	for _, run := range w.u.hist.chart(in.Width, in.Height) {
		pts := make([]walk.Point, len(run))
		for i, q := range run {
			pts[i] = walk.Point{X: in.X + q.X, Y: in.Y + q.Y}
		}
		if len(pts) == 1 {
			// A single measurement between gaps is still a measurement.
			p.fill(walk.Rectangle{X: pts[0].X - p.s(1), Y: pts[0].Y - p.s(1), Width: p.s(3), Height: p.s(3)}, accent)
			continue
		}
		_ = p.c.DrawPolylinePixels(pen, pts)
	}
}

func (w *panels) paintSpark(p *painter, r walk.Rectangle, accent walk.Color) {
	levels := w.u.hist.spark(sparkBars)
	step := r.Width / len(levels)
	bar := max(step-p.s(2), 1)
	for i, l := range levels {
		bx := r.X + i*step
		if l < 0 {
			p.fill(walk.Rectangle{X: bx, Y: r.Y + r.Height - p.s(2), Width: bar, Height: p.s(2)}, cRule)
			continue
		}
		h := (l + 1) * r.Height / sparkLevels
		p.fill(walk.Rectangle{X: bx, Y: r.Y + r.Height - h, Width: bar, Height: h}, accent)
	}
}

func rttColour(v view) walk.Color {
	if v.rtt == "--" {
		return cDim
	}
	return cBright
}

func problemLine(v view) string {
	if v.problem == "" {
		return ""
	}
	return "! " + v.problem
}

// painter draws in native pixels; s scales a 96-dpi length to them.
type painter struct {
	c   *walk.Canvas
	dpi int
	b   walk.Rectangle
}

func (p *painter) s(v int) int { return walk.IntFrom96DPI(v, p.dpi) }

func (p *painter) fill(r walk.Rectangle, c walk.Color) {
	brush, err := walk.NewSolidColorBrush(c)
	if err != nil {
		return
	}
	_ = p.c.FillRectanglePixels(brush, r)
	brush.Dispose()
}

func (p *painter) frame(r walk.Rectangle) {
	pen, err := walk.NewCosmeticPen(walk.PenSolid, cRule)
	if err != nil {
		return
	}
	_ = p.c.DrawRectanglePixels(pen, r)
	pen.Dispose()
}

func (p *painter) inset(r walk.Rectangle, d int) walk.Rectangle {
	return walk.Rectangle{X: r.X + d, Y: r.Y + d, Width: r.Width - 2*d, Height: r.Height - 2*d}
}

func (p *painter) text(s string, f *walk.Font, c walk.Color, r walk.Rectangle, align walk.DrawTextFormat) {
	_ = p.c.DrawTextPixels(s, f, c, r, align|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
}
