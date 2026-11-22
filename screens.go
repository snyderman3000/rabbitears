package main

// Everything Rabbit Ears draws itself: snow, the CRT power on/off, the
// on-screen channel number (rendered to a PNG that ffplay overlays), the
// channel guide, colour bars and NO SIGNAL.

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"strings"
	"time"
)

var (
	osdGreen  = rgb(0x52, 0xff, 0x4a)
	osdShadow = rgb(0x00, 0x18, 0x00)

	guideNavy   = rgb(0x0a, 0x16, 0x5c)
	guideBlue   = rgb(0x18, 0x34, 0x9c)
	guideBlue2  = rgb(0x22, 0x46, 0xc0)
	guideCyan   = rgb(0x6c, 0xe4, 0xff)
	guideYellow = rgb(0xff, 0xe0, 0x3a)
	guideWhite  = rgb(0xf6, 0xf6, 0xff)
	guideDim    = rgb(0x9a, 0xa8, 0xe0)
)

type Fonts struct {
	vt   map[int]*Font // VT323 by pixel size
	body map[int]*Font
	head map[int]*Font
}

func newFonts() *Fonts {
	return &Fonts{vt: map[int]*Font{}, body: map[int]*Font{}, head: map[int]*Font{}}
}

func (f *Fonts) VT(px int) *Font {
	if f.vt[px] == nil {
		f.vt[px] = loadFont(lcdTTF, float64(px))
	}
	return f.vt[px]
}

func (f *Fonts) Body(px int) *Font {
	if f.body[px] == nil {
		f.body[px] = loadFont(bodyTTF, float64(px))
	}
	return f.body[px]
}

func (f *Fonts) Head(px int) *Font {
	if f.head[px] == nil {
		f.head[px] = loadFont(titleTTF, float64(px))
	}
	return f.head[px]
}

// ---------- snow ----------

type Snow struct {
	s     uint32
	frame int
}

// Draw fills the canvas with analog snow: chunky grain, faint horizontal
// streaks and a slow brighter band rolling down, like an untuned set.
func (n *Snow) Draw(c *Canvas) {
	n.frame++
	if n.s == 0 {
		n.s = uint32(time.Now().UnixNano()) | 1
	}
	s := n.s
	band := float64(n.frame%90) / 90 * float64(c.H+160)
	for y := 0; y < c.H; y += 2 {
		// a few rows get a horizontal streak
		s ^= s << 13
		s ^= s >> 17
		s ^= s << 5
		streak := 0
		if s%23 == 0 {
			streak = int(s>>27) + 18
		}
		d := math.Abs(float64(y) - band + 80)
		lift := 0
		if d < 80 {
			lift = int((80 - d) / 80 * 38)
		}
		o := y * c.Stride
		o2 := o + c.Stride
		for x := 0; x < c.W; x += 2 {
			s ^= s << 13
			s ^= s >> 17
			s ^= s << 5
			v := int(s>>24)*3/4 + lift + streak
			if v > 255 {
				v = 255
			}
			b := v + 6
			if b > 255 {
				b = 255
			}
			for _, p := range [2]int{o + x*4, o2 + x*4} {
				if p+7 >= len(c.Pix) {
					continue
				}
				c.Pix[p], c.Pix[p+1], c.Pix[p+2], c.Pix[p+3] = uint8(v), uint8(v), uint8(b), 255
				c.Pix[p+4], c.Pix[p+5], c.Pix[p+6], c.Pix[p+7] = uint8(v), uint8(v), uint8(b), 255
			}
		}
	}
	n.s = s
}

// ---------- CRT power on / off ----------

// PowerOnFrame: a bright line opens up into the picture. t runs 0→1.
func PowerOnFrame(c *Canvas, under *Canvas, t float64) {
	c.Fill(0, 0, c.W, c.H, colBlack)
	cy := c.H / 2
	if t < 0.35 {
		k := t / 0.35
		w := int(float64(c.W) * easeOut(k))
		c.Fill((c.W-w)/2, cy-1, w, 2, rgb(0xd8, 0xe8, 0xff))
		c.Blend((c.W-w)/2, cy-4, w, 8, rgb(0x90, 0xb0, 0xff), 0.35*k)
		return
	}
	k := easeOut((t - 0.35) / 0.65)
	h := int(float64(c.H) * k)
	if h < 2 {
		h = 2
	}
	// squeeze the full picture into the opening band
	for y := 0; y < h; y++ {
		sy := y * c.H / h
		copy(c.Pix[(cy-h/2+y)*c.Stride:(cy-h/2+y+1)*c.Stride], under.Pix[sy*under.Stride:(sy+1)*under.Stride])
	}
	c.Blend(0, cy-h/2, c.W, h, rgb(0xff, 0xff, 0xff), 0.5*(1-k))
}

// PowerOffFrame: the picture collapses to a line, then a dot that fades.
func PowerOffFrame(c *Canvas, last *Canvas, t float64) {
	c.Fill(0, 0, c.W, c.H, colBlack)
	cx, cy := c.W/2, c.H/2
	switch {
	case t < 0.45:
		k := easeIn(t / 0.45)
		h := int(float64(c.H) * (1 - k))
		if h < 2 {
			h = 2
		}
		for y := 0; y < h; y++ {
			sy := y * c.H / h
			copy(c.Pix[(cy-h/2+y)*c.Stride:(cy-h/2+y+1)*c.Stride], last.Pix[sy*last.Stride:(sy+1)*last.Stride])
		}
		c.Blend(0, cy-h/2, c.W, h, rgb(0xff, 0xff, 0xff), 0.15+0.7*k)
	case t < 0.75:
		k := easeIn((t - 0.45) / 0.3)
		w := int(float64(c.W) * (1 - k))
		if w < 4 {
			w = 4
		}
		c.Blend(cx-w/2, cy-4, w, 8, rgb(0x90, 0xb0, 0xff), 0.3)
		c.Fill(cx-w/2, cy-1, w, 2, rgb(0xf0, 0xf6, 0xff))
	default:
		k := (t - 0.75) / 0.25
		r := int(5 * (1 - k))
		if r > 0 {
			c.Circle(cx, cy, r+3, mix(colBlack, rgb(0x70, 0x90, 0xff), 0.4*(1-k)), true, 0)
			c.Circle(cx, cy, r, mix(colBlack, rgb(0xff, 0xff, 0xff), 1-k*0.8), true, 0)
		}
	}
}

func easeOut(t float64) float64 { t = clamp01(t); return 1 - (1-t)*(1-t) }
func easeIn(t float64) float64  { t = clamp01(t); return t * t }
func clamp01(t float64) float64 { return math.Max(0, math.Min(1, t)) }

// ---------- on-screen display (PNG for ffplay) ----------

// drawOver composites text onto a transparent NRGBA image.
func drawOver(img *image.NRGBA, f *Font, x, y int, s string, col color.RGBA) int {
	base := y + f.ascent
	b := img.Bounds()
	for _, r := range s {
		g := f.glyph(r)
		if g.mask != nil {
			gx, gy := x+g.off.X, base+g.off.Y
			mb := g.mask.Bounds()
			for yy := 0; yy < mb.Dy(); yy++ {
				py := gy + yy
				if py < 0 || py >= b.Dy() {
					continue
				}
				for xx := 0; xx < mb.Dx(); xx++ {
					px := gx + xx
					if px < 0 || px >= b.Dx() {
						continue
					}
					a := int(g.mask.Pix[yy*g.mask.Stride+xx])
					if a < 96 { // crisp, TV-style edges
						continue
					}
					o := py*img.Stride + px*4
					img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = col.R, col.G, col.B, 255
				}
			}
		}
		x += g.advance
	}
	return x
}

func osdTextShadow(img *image.NRGBA, f *Font, x, y int, s string, col color.RGBA, sh int) {
	for _, d := range [][2]int{{sh, sh}, {sh, 0}, {0, sh}} {
		drawOver(img, f, x+d[0], y+d[1], s, osdShadow)
	}
	drawOver(img, f, x, y, s, col)
}

// fitText trims s with "..." until it fits in w pixels.
func fitText(f *Font, s string, w int) string {
	if f.Width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 1 && f.Width(string(r)+"...") > w {
		r = r[:len(r)-1]
	}
	return strings.TrimSpace(string(r)) + "..."
}

func osdWidth(f *Font, s string) int { return f.Width(s) }

// RenderOSD draws the channel number (top right) and, for the DISPLAY
// button, the programme and clock along the bottom, sized for a w×h video.
func RenderOSD(fonts *Fonts, w, h int, ch *Channel, info *Airing, note string, now time.Time) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	u := float64(h) / 240
	px := func(v float64) int { return int(math.Round(v * u)) }
	sh := px(1.5)
	if sh < 1 {
		sh = 1
	}
	big := fonts.VT(px(46))
	small := fonts.VT(px(18))
	num := fmt.Sprintf("%d", ch.Num)
	right := w - px(16)
	top := px(10)
	nw := osdWidth(big, num)
	osdTextShadow(img, big, right-nw, top, num, osdGreen, sh)
	cw := osdWidth(small, "CH")
	osdTextShadow(img, small, right-nw-cw-px(4), top+px(8), "CH", osdGreen, sh)
	name := fitText(small, strings.ToUpper(ch.Name), w*3/4)
	osdTextShadow(img, small, right-osdWidth(small, name), top+px(44), name, osdGreen, sh)

	if info != nil {
		y := h - px(52)
		left := px(14)
		clock := strings.ToUpper(now.Format("3:04 PM"))
		osdTextShadow(img, small, left, y, clock, osdGreen, sh)
		left2 := fmt.Sprintf("%s LEFT", fmtDur(info.End.Sub(now)))
		title := fitText(small, strings.ToUpper(info.Item.Label()), w-left*2-osdWidth(small, left2)-px(12))
		if note != "" {
			osdTextShadow(img, small, left, y-px(18), fitText(small, note, w-left*2), osdGreen, sh)
		}
		osdTextShadow(img, small, left, y+px(18), title, osdGreen, sh)
		// a row of blocks showing how far into the programme we are
		span := info.End.Sub(info.Start).Seconds()
		done := 0.0
		if span > 0 {
			done = now.Sub(info.Start).Seconds() / span
		}
		bw, bh, n := px(6), px(5), 20
		for i := 0; i < n; i++ {
			x := right - (n-i)*(bw+px(2))
			col := rgb(0x1c, 0x5a, 0x1a)
			if float64(i)/float64(n) < done {
				col = osdGreen
			}
			for yy := 0; yy < bh; yy++ {
				for xx := 0; xx < bw; xx++ {
					o := (y+px(8)+yy)*img.Stride + (x+xx)*4
					if o >= 0 && o+3 < len(img.Pix) {
						img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = col.R, col.G, col.B, 255
					}
				}
			}
		}
		osdTextShadow(img, small, right-osdWidth(small, left2), y+px(18), left2, osdGreen, sh)
	}
	return img
}

// RenderPlayOSD: the VCR-style "PLAY" (or resume point) shown when a DVR
// episode starts.
func RenderPlayOSD(fonts *Fonts, w, h int, title string, from float64) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	u := float64(h) / 240
	px := func(v float64) int { return int(math.Round(v * u)) }
	sh := max(px(1.5), 1)
	big := fonts.VT(px(30))
	small := fonts.VT(px(18))
	label := "PLAY"
	if from > 0 {
		label = "PLAY  " + fmtClock(from)
	}
	x := px(14)
	// a solid "play" triangle
	tri := px(16)
	for i := 0; i < tri; i++ {
		hh := tri - i
		for yy := -hh / 2; yy <= hh/2; yy++ {
			for _, d := range [][2]int{{sh, sh}, {0, 0}} {
				col := osdShadow
				if d[0] == 0 {
					col = osdGreen
				}
				o := (px(14)+tri/2+yy+d[1])*img.Stride + (x+i+d[0])*4
				if o >= 0 && o+3 < len(img.Pix) {
					img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = col.R, col.G, col.B, 255
				}
			}
		}
	}
	osdTextShadow(img, big, x+tri+px(6), px(10), label, osdGreen, sh)
	osdTextShadow(img, small, x, h-px(30), fitText(small, strings.ToUpper(title), w-2*x), osdGreen, sh)
	return img
}

// fmtClock: 75 → "1:15", 3725 → "1:02:05".
func fmtClock(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	t := int(sec + 0.5)
	if t >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", t/3600, t/60%60, t%60)
	}
	return fmt.Sprintf("%d:%02d", t/60, t%60)
}

func fmtDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	m := int(d.Minutes() + 0.5)
	if m >= 60 {
		return fmt.Sprintf("%d:%02d", m/60, m%60)
	}
	if m < 1 {
		return "<1 MIN"
	}
	return fmt.Sprintf("%d MIN", m)
}

func writePNG(path string, img image.Image) error {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, img); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// ---------- channel guide ----------

type Guide struct {
	Sel, Top int
}

const guideRows = 5

func DrawGuide(c *Canvas, fonts *Fonts, chans []*Channel, g *Guide, now time.Time, cur int) {
	W := c.W
	// background: deep navy with a subtle vertical gradient
	for y := 0; y < c.H; y++ {
		c.Fill(0, y, W, 1, mix(rgb(0x06, 0x0c, 0x38), guideNavy, float64(y)/float64(c.H)))
	}
	// header
	c.Fill(0, 0, W, 64, rgb(0x04, 0x08, 0x2a))
	c.Fill(0, 62, W, 2, guideCyan)
	hf := fonts.Head(22)
	c.Text(hf, 18, 10, "RABBIT EARS", guideYellow)
	c.Text(fonts.Body(15), 20, 38, "CHANNEL GUIDE", guideCyan)
	clock := now.Format("3:04")
	ampm := now.Format("PM")
	vt := fonts.VT(44)
	cw := vt.Width(clock)
	c.Text(vt, W-24-cw-fonts.VT(20).Width(ampm)-4, 6, clock, guideWhite)
	c.Text(fonts.VT(20), W-24-fonts.VT(20).Width(ampm), 28, ampm, guideWhite)

	// column heads
	slotStart := now.Truncate(30 * time.Minute)
	colX := []int{196, 420}
	c.Fill(0, 64, W, 26, rgb(0x0e, 0x22, 0x80))
	c.Text(fonts.Body(15), 18, 68, "CH", guideDim)
	c.Text(fonts.Body(15), colX[0]+10, 68, "ON NOW", guideYellow)
	c.Text(fonts.Body(15), colX[1]+10, 68, "UP NEXT", guideDim)
	_ = slotStart

	rowH := 71
	y0 := 92
	if g.Sel < g.Top {
		g.Top = g.Sel
	}
	if g.Sel >= g.Top+guideRows {
		g.Top = g.Sel - guideRows + 1
	}
	titleF := fonts.Body(17)
	timeF := fonts.VT(22)
	for r := 0; r < guideRows; r++ {
		i := g.Top + r
		y := y0 + r*rowH
		if i >= len(chans) {
			break
		}
		ch := chans[i]
		sel := i == g.Sel
		// channel box
		box := guideBlue
		if sel {
			box = guideBlue2
		}
		c.Fill(8, y, colX[0]-12, rowH-4, box)
		c.Text(fonts.VT(40), 16, y+8, fmt.Sprintf("%d", ch.Num), guideYellow)
		nf := fonts.Body(15)
		lines := nf.Wrap(strings.ToUpper(ch.Name), colX[0]-12-66)
		if len(lines) > 2 {
			lines = lines[:2]
			lines[1] = fitText(nf, lines[1]+" ...", colX[0]-12-66)
		}
		ly := y + 14
		if len(lines) == 1 {
			ly = y + 24
		}
		if i == cur {
			ly = y + 12
			if len(lines) > 1 {
				lines = lines[:1]
				lines[0] = fitText(nf, lines[0], colX[0]-12-66)
			}
		}
		for _, ln := range lines {
			c.Text(nf, 66, ly, ln, guideWhite)
			ly += 20
		}
		if i == cur {
			c.Fill(66, y+40, 7, 7, guideCyan)
			c.Text(fonts.Body(13), 78, y+36, "TUNED", guideCyan)
		}
		air := ch.Upcoming(now, 2)
		for k, a := range air {
			x := colX[k]
			w := W - 8 - x
			if k == 0 {
				w = colX[1] - 4 - x
			}
			cell := guideBlue
			if sel {
				cell = guideBlue2
			}
			c.Fill(x, y, w, rowH-4, cell)
			tl := titleF.Wrap(a.Item.Label(), w-20)
			if len(tl) > 2 {
				tl = tl[:2]
				tl[1] = fitText(titleF, tl[1]+" ...", w-20)
			}
			for j, ln := range tl {
				c.Text(titleF, x+10, y+4+j*19, ln, guideWhite)
			}
			var when string
			if k == 0 {
				when = fmtDur(a.End.Sub(now)) + " LEFT"
				// progress strip
				span := a.End.Sub(a.Start).Seconds()
				if span > 0 {
					p := now.Sub(a.Start).Seconds() / span
					c.Fill(x+10, y+rowH-9, w-20, 3, rgb(0x0a, 0x1a, 0x60))
					c.Fill(x+10, y+rowH-9, int(float64(w-20)*clamp01(p)), 3, guideCyan)
				}
			} else {
				when = strings.ToUpper(a.Start.Format("3:04 PM"))
			}
			c.Text(timeF, x+10, y+43, when, guideDim)
		}
		if sel {
			c.Border(6, y-2, W-12, rowH, 2, guideYellow)
		}
	}
	// scroll position
	if len(chans) > guideRows {
		pos := fmt.Sprintf("%d OF %d", g.Sel+1, len(chans))
		c.TextRight(fonts.Body(14), W-18, 69, pos, guideDim)
	}
	// footer
	fy := c.H - 30
	c.Fill(0, fy-2, W, 2, guideCyan)
	c.Fill(0, fy, W, 30, rgb(0x04, 0x08, 0x2a))
	bf := fonts.Body(14)
	x := c.Button(bf, 18, fy+6, "A", "WATCH", guideWhite)
	x = c.Button(bf, x+8, fy+6, "B", "BACK", guideWhite)
	x = c.Button(bf, x+8, fy+6, "START", "SETUP", guideWhite)
	c.Button(bf, x+8, fy+6, "MENU", "TURN OFF", guideWhite)
}

// ---------- colour bars / messages ----------

func DrawBars(c *Canvas, fonts *Fonts, title, line1, line2 string) {
	W, H := c.W, c.H
	top := []color.RGBA{rgb(192, 192, 192), rgb(192, 192, 0), rgb(0, 192, 192), rgb(0, 192, 0), rgb(192, 0, 192), rgb(192, 0, 0), rgb(0, 0, 192)}
	mid := []color.RGBA{rgb(0, 0, 192), rgb(19, 19, 19), rgb(192, 0, 192), rgb(19, 19, 19), rgb(0, 192, 192), rgb(19, 19, 19), rgb(192, 192, 192)}
	h1 := H * 67 / 100
	h2 := H * 8 / 100
	for i := 0; i < 7; i++ {
		x0, x1 := i*W/7, (i+1)*W/7
		c.Fill(x0, 0, x1-x0, h1, top[i])
		c.Fill(x0, h1, x1-x0, h2, mid[i])
	}
	bot := []struct {
		w   int
		col color.RGBA
	}{{5, rgb(0, 33, 76)}, {5, rgb(255, 255, 255)}, {5, rgb(50, 0, 106)}, {5, rgb(19, 19, 19)}, {2, rgb(9, 9, 9)}, {1, rgb(19, 19, 19)}, {1, rgb(29, 29, 29)}, {4, rgb(19, 19, 19)}}
	x := 0
	for i, b := range bot {
		w := b.w * W / 28
		if i == len(bot)-1 {
			w = W - x
		}
		c.Fill(x, h1+h2, w, H-h1-h2, b.col)
		x += w
	}
	if title != "" {
		bw, bh := 440, 116
		bx, by := (W-bw)/2, H/2-bh/2-40
		c.Fill(bx, by, bw, bh, rgb(0, 0, 0))
		c.Border(bx+4, by+4, bw-8, bh-8, 2, rgb(0xe8, 0xe8, 0xe8))
		c.TextCenter(fonts.VT(40), W/2, by+12, title, rgb(0xf4, 0xf4, 0xf4))
		c.TextCenter(fonts.Body(15), W/2, by+62, line1, rgb(0xc8, 0xc8, 0xc8))
		c.TextCenter(fonts.Body(15), W/2, by+84, line2, rgb(0xc8, 0xc8, 0xc8))
	}
}

// DrawNoSignal: snow with a box explaining why the channel won't play.
func DrawNoSignal(c *Canvas, fonts *Fonts, snow *Snow, ch *Channel, why string) {
	snow.Draw(c)
	bw, bh := 460, 128
	bx, by := (c.W-bw)/2, (c.H-bh)/2
	c.Blend(bx, by, bw, bh, colBlack, 0.82)
	c.TextCenter(fonts.VT(40), c.W/2, by+10, "NO SIGNAL", rgb(0xf0, 0xf0, 0xf0))
	if ch != nil {
		c.TextCenter(fonts.VT(22), c.W/2, by+52, fmt.Sprintf("CH %d  %s", ch.Num, strings.ToUpper(ch.Name)), osdGreen)
	}
	f := fonts.Body(13)
	c.TextCenter(f, c.W/2, by+84, f.Ellipsize(why, bw-24), rgb(0xc0, 0xc0, 0xc0))
	c.TextCenter(f, c.W/2, by+104, "UP/DOWN: CHANGE CHANNEL", rgb(0x90, 0x90, 0x90))
}
