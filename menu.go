package main

// The SETUP menu (START): live TV or DVR, commercials, show order. Styled
// like the blue on-screen menu of a 90s TV.

import (
	"fmt"
	"image/color"
	"strings"
)

var breakChoices = []float64{6, 8, 10, 12, 15, 0} // 0 = between shows only
var orderChoices = []string{OrderRotate, OrderSequential, OrderShuffle}

const (
	rowMode = iota
	rowAds
	rowEvery
	rowPer
	rowOrder
	rowDone
	menuRows
)

type Menu struct {
	Sel   int
	mode  int // 0 live, 1 dvr
	ads   bool
	every int // index in breakChoices
	per   int
	order int // index in orderChoices
	found int // commercials on the card
}

func (m *Menu) open(cfg Config, found int) {
	m.Sel = rowMode
	m.mode = 0
	if cfg.Mode == ModeDVR {
		m.mode = 1
	}
	m.ads = cfg.Commercials
	m.every = 1
	best := 1e9
	for i, v := range breakChoices {
		d := v - cfg.AdsEveryMin
		if d < 0 {
			d = -d
		}
		if d < best {
			best, m.every = d, i
		}
	}
	m.per = cfg.AdsPerBreak
	m.order = 0
	for i, o := range orderChoices {
		if o == cfg.Order {
			m.order = i
		}
	}
	m.found = found
}

func (m *Menu) apply(cfg Config) Config {
	cfg.Mode = ModeLive
	if m.mode == 1 {
		cfg.Mode = ModeDVR
	}
	cfg.Commercials = m.ads
	cfg.AdsEveryMin = breakChoices[m.every]
	cfg.AdsPerBreak = m.per
	cfg.Order = orderChoices[m.order]
	return cfg
}

func (m *Menu) enabled(row int) bool {
	if row == rowEvery || row == rowPer {
		return m.ads && m.mode == 0
	}
	if row == rowAds {
		return m.mode == 0
	}
	return true
}

func (m *Menu) move(d int) {
	for k := 0; k < menuRows; k++ {
		m.Sel = (m.Sel + d + menuRows) % menuRows
		if m.enabled(m.Sel) {
			return
		}
	}
}

func (m *Menu) change(d int) {
	switch m.Sel {
	case rowMode:
		m.mode = (m.mode + d + 2) % 2
	case rowAds:
		m.ads = !m.ads
	case rowEvery:
		m.every = (m.every + d + len(breakChoices)) % len(breakChoices)
	case rowPer:
		m.per = min(6, max(1, m.per+d))
	case rowOrder:
		m.order = (m.order + d + len(orderChoices)) % len(orderChoices)
	}
}

// key handles a button; true means close the menu.
func (m *Menu) key(k int) bool {
	switch k {
	case BtnUp:
		m.move(-1)
	case BtnDown:
		m.move(1)
	case BtnLeft, BtnL1:
		m.change(-1)
	case BtnRight, BtnR1:
		m.change(1)
	case BtnA:
		if m.Sel == rowDone {
			return true
		}
		m.change(1)
	case BtnB, BtnStart, BtnSelect:
		return true
	}
	return false
}

func (m *Menu) label(row int) (string, string) {
	switch row {
	case rowMode:
		return "MODE", []string{"LIVE TV", "DVR"}[m.mode]
	case rowAds:
		if m.ads {
			return "COMMERCIALS", "ON"
		}
		return "COMMERCIALS", "OFF"
	case rowEvery:
		if breakChoices[m.every] == 0 {
			return "BREAKS", "BETWEEN SHOWS"
		}
		return "BREAKS", fmt.Sprintf("EVERY %.0f MIN", breakChoices[m.every])
	case rowPer:
		return "ADS PER BREAK", fmt.Sprint(m.per)
	case rowOrder:
		return "SHOW ORDER", map[string]string{OrderRotate: "ROTATE SHOWS", OrderSequential: "IN ORDER", OrderShuffle: "SHUFFLE"}[orderChoices[m.order]]
	}
	return "DONE", ""
}

func (m *Menu) help() string {
	switch m.Sel {
	case rowMode:
		if m.mode == 1 {
			return "DVR: pick any show and episode and watch it from the start. Picks up where you left off. No commercials."
		}
		return "Live TV: every channel is always on the air. Tune in and you land mid-show."
	case rowAds:
		if m.found == 0 {
			return "No commercials found yet. Put them in Media/TV/Commercials (a folder named after a channel is just for that channel)."
		}
		return fmt.Sprintf("%d commercials found in Media/TV/Commercials. They play before each show and in breaks.", m.found)
	case rowEvery:
		return "How often a show stops for a break. Breaks never land in the first or last few minutes."
	case rowPer:
		return "How many commercials play in each break."
	case rowOrder:
		switch orderChoices[m.order] {
		case OrderRotate:
			return "A channel with show folders airs one episode of each show in turn, in episode order."
		case OrderShuffle:
			return "Episodes air in a random order (an order.txt in a channel folder overrides this)."
		}
		return "Every episode in the channel folder airs in order, folder by folder."
	}
	return "Save and go back to the TV."
}

func DrawMenu(c *Canvas, fonts *Fonts, m *Menu) {
	bg := rgb(0x10, 0x1c, 0xa8)
	white := rgb(0xf4, 0xf6, 0xff)
	dim := rgb(0x7a, 0x86, 0xd0)
	cyan := rgb(0x6c, 0xe4, 0xff)
	c.Fill(0, 0, c.W, c.H, bg)
	c.Scanlines(0, 0, c.W, c.H, 0.10)

	title := fonts.VT(56)
	c.Text(title, 36, 14, "SETUP", white)
	c.TextRight(fonts.VT(26), c.W-36, 34, "RABBIT EARS "+version, dim)
	c.Fill(36, 72, c.W-72, 3, white)

	row := fonts.VT(34)
	y := 92
	for r := 0; r < menuRows; r++ {
		lab, val := m.label(r)
		col := white
		if !m.enabled(r) {
			col = dim
		}
		h := 46
		if r == rowDone {
			y += 8
		}
		if r == m.Sel {
			c.Fill(28, y-2, c.W-56, h, cyan)
			col = bg
		}
		c.Text(row, 44, y+4, lab, col)
		if val != "" {
			vw := row.Width(val)
			x := c.W - 74 - vw
			c.Text(row, x, y+4, val, col)
			if r == m.Sel {
				arrow(c, x-22, y+h/2-1, -1, col)
				arrow(c, c.W-58, y+h/2-1, 1, col)
			}
		}
		y += h
	}

	// help text
	hy := c.H - 92
	c.Fill(36, hy-10, c.W-72, 2, dim)
	hf := fonts.Body(16)
	for i, ln := range hf.Wrap(strings.ToUpper(m.help()), c.W-72) {
		if i > 2 {
			break
		}
		c.Text(hf, 36, hy+i*22, ln, white)
	}
}

// arrow draws a small solid triangle pointing left (d<0) or right.
func arrow(c *Canvas, x, cy, d int, col color.RGBA) {
	for i := 0; i < 9; i++ {
		h := 9 - i
		xx := x + i
		if d < 0 {
			xx = x + 8 - i
		}
		c.Fill(xx, cy-h, 1, h*2, col)
	}
}
