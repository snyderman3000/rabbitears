package main

// DVR mode: browse channel → show → season → episode and watch anything on
// demand. Rabbit Ears remembers how far you got in each episode.
//
// While an episode plays, Onion's FFplay owns the buttons (d-pad seeks, A
// pauses, X/Y jump 10 minutes, MENU stops). Rabbit Ears sees the same button
// presses, so it follows along to know where you stopped.

import (
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Node struct {
	Name   string
	Kids   []*Node
	Item   *Item
	Ch     *Channel
	Parent *Node
}

func (n *Node) isFolder() bool { return n.Item == nil }

// episodes lists every video under n.
func (n *Node) episodes(out []*Item) []*Item {
	if n.Item != nil {
		return append(out, n.Item)
	}
	for _, k := range n.Kids {
		out = k.episodes(out)
	}
	return out
}

type Mark struct {
	Pos  float64 `json:"p,omitempty"`
	Done bool    `json:"d,omitempty"`
	When int64   `json:"t,omitempty"`
}

type level struct {
	node     *Node
	sel, top int
}

const (
	dvrNone = iota
	dvrPlay
	dvrPlayFromStart
	dvrLive
	dvrMenu
)

const dvrRows = 6

type DVR struct {
	path    string
	tvDir   string
	marks   map[string]Mark
	root    *Node
	nodeOf  map[*Item]*Node
	stack   []level
	message string

	playing *Node
	track   tracker
}

func newDVR(path string) *DVR {
	d := &DVR{path: path, marks: map[string]Mark{}, root: &Node{Name: "RECORDINGS"}}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &d.marks)
	}
	d.stack = []level{{node: d.root}}
	return d
}

func (d *DVR) save() {
	if d.path == "" {
		return
	}
	if b, err := json.Marshal(d.marks); err == nil {
		tmp := d.path + ".tmp"
		if os.WriteFile(tmp, b, 0o644) == nil {
			os.Rename(tmp, d.path)
		}
	}
}

func (d *DVR) key(it *Item) string {
	if rel, err := filepath.Rel(d.tvDir, it.Path); err == nil {
		return filepath.ToSlash(rel)
	}
	return it.Path
}

func (d *DVR) mark(it *Item) Mark { return d.marks[d.key(it)] }

// setLineup rebuilds the browser tree, keeping the place you were at.
func (d *DVR) setLineup(tvDir string, chans []*Channel) {
	d.tvDir = tvDir
	var where []string
	for _, l := range d.stack[1:] {
		where = append(where, l.node.Name)
	}
	d.root = &Node{Name: "RECORDINGS"}
	d.nodeOf = map[*Item]*Node{}
	for _, ch := range chans {
		cn := &Node{Name: fmt.Sprintf("%d  %s", ch.Num, ch.Name), Ch: ch, Parent: d.root}
		d.root.Kids = append(d.root.Kids, cn)
		if ch.Dir == "" {
			cn.Item = ch.Items[0]
			d.nodeOf[cn.Item] = cn
			continue
		}
		items := append([]*Item(nil), ch.Items...)
		sort.SliceStable(items, func(i, j int) bool { return natLess(items[i].Rel, items[j].Rel) })
		for _, it := range items {
			parts := strings.Split(it.Rel, "/")
			at := cn
			for _, p := range parts[:len(parts)-1] {
				_, nm := splitNumber(p)
				nm = cleanFolder(nm)
				var next *Node
				for _, k := range at.Kids {
					if k.isFolder() && k.Name == nm {
						next = k
					}
				}
				if next == nil {
					next = &Node{Name: nm, Ch: ch, Parent: at}
					at.Kids = append(at.Kids, next)
				}
				at = next
			}
			n := &Node{Name: it.Title, Item: it, Ch: ch, Parent: at}
			at.Kids = append(at.Kids, n)
			d.nodeOf[it] = n
		}
	}
	// back to where we were, as far as it still exists
	d.stack = []level{{node: d.root}}
	for _, name := range where {
		cur := d.stack[len(d.stack)-1].node
		found := -1
		for i, k := range cur.Kids {
			if k.isFolder() && k.Name == name {
				found = i
			}
		}
		if found < 0 {
			break
		}
		d.stack[len(d.stack)-1].sel = found
		d.stack = append(d.stack, level{node: cur.Kids[found]})
	}
}

func (d *DVR) top() *level { return &d.stack[len(d.stack)-1] }

func (d *DVR) selected() *Node {
	l := d.top()
	if l.sel < 0 || l.sel >= len(l.node.Kids) {
		return nil
	}
	return l.node.Kids[l.sel]
}

// focus opens the folder holding n and selects it.
func (d *DVR) focus(n *Node) {
	var chain []*Node
	for p := n; p != nil; p = p.Parent {
		chain = append([]*Node{p}, chain...)
	}
	if len(chain) == 0 || chain[0] != d.root {
		return
	}
	d.stack = nil
	for i := 0; i < len(chain)-1; i++ {
		l := level{node: chain[i]}
		for j, k := range chain[i].Kids {
			if k == chain[i+1] {
				l.sel = j
			}
		}
		d.stack = append(d.stack, l)
	}
	d.fixScroll()
}

func (d *DVR) fixScroll() {
	l := d.top()
	if l.sel < l.top {
		l.top = l.sel
	}
	if l.sel >= l.top+dvrRows {
		l.top = l.sel - dvrRows + 1
	}
	if l.top < 0 {
		l.top = 0
	}
}

// browserKey handles a button in the browser and says what to do next.
func (d *DVR) browserKey(k int) int {
	d.message = ""
	l := d.top()
	n := len(l.node.Kids)
	switch k {
	case BtnUp:
		if n > 0 {
			l.sel = (l.sel - 1 + n) % n
		}
	case BtnDown:
		if n > 0 {
			l.sel = (l.sel + 1) % n
		}
	case BtnL1, BtnLeft:
		l.sel = max(0, l.sel-dvrRows)
	case BtnR1, BtnRight:
		l.sel = min(n-1, l.sel+dvrRows)
	case BtnA:
		s := d.selected()
		if s == nil {
			break
		}
		if s.isFolder() {
			d.stack = append(d.stack, level{node: s})
			break
		}
		return dvrPlay
	case BtnY:
		if s := d.selected(); s != nil && !s.isFolder() {
			return dvrPlayFromStart
		}
	case BtnX:
		s := d.selected()
		if s == nil {
			break
		}
		eps := s.episodes(nil)
		all := true
		for _, it := range eps {
			all = all && d.mark(it).Done
		}
		for _, it := range eps {
			if all {
				delete(d.marks, d.key(it))
			} else {
				d.marks[d.key(it)] = Mark{Done: true, When: time.Now().Unix()}
			}
		}
		d.save()
	case BtnB:
		if len(d.stack) > 1 {
			d.stack = d.stack[:len(d.stack)-1]
		}
	case BtnSelect:
		return dvrLive
	case BtnStart:
		return dvrMenu
	}
	d.fixScroll()
	return dvrNone
}

// resumeAt is where an episode should start: where you stopped, a few
// seconds early, unless you'd nearly finished it.
func (d *DVR) resumeAt(it *Item) float64 {
	m := d.mark(it)
	if m.Done || m.Pos < 20 {
		return 0
	}
	if it.Dur > 0 && m.Pos > it.Dur-45 {
		return 0
	}
	return m.Pos - 3
}

func (d *DVR) startPlaying(n *Node, ss float64) {
	d.playing = n
	d.track = tracker{pos: ss, last: time.Now(), dur: n.Item.Dur}
}

// stopPlaying records where you got to (finished: the episode played out).
func (d *DVR) stopPlaying(finished bool) {
	n := d.playing
	if n == nil {
		return
	}
	d.playing = nil
	k := d.key(n.Item)
	if finished {
		d.marks[k] = Mark{Done: true, When: time.Now().Unix()}
	} else {
		pos := d.track.now()
		m := d.marks[k]
		m.Pos, m.When = pos, time.Now().Unix()
		if n.Item.Dur > 0 && pos > n.Item.Dur-45 {
			m.Done, m.Pos = true, 0
		}
		d.marks[k] = m
	}
	d.save()
	d.focus(n)
}

// nextAfter: the next episode of the same show, across season folders.
func (d *DVR) nextAfter(n *Node) *Node {
	if n == nil || n.Ch == nil || n.Ch.Dir == "" {
		return nil
	}
	items := append([]*Item(nil), n.Ch.Items...)
	sort.SliceStable(items, func(i, j int) bool { return natLess(items[i].Rel, items[j].Rel) })
	for i, it := range items {
		if it != n.Item {
			continue
		}
		for _, nx := range items[i+1:] {
			if nx.Show == it.Show {
				return d.nodeOf[nx]
			}
		}
	}
	return nil
}

// ---------- following FFplay ----------

type tracker struct {
	pos    float64
	dur    float64
	last   time.Time
	paused bool
}

func (t *tracker) update() {
	now := time.Now()
	if !t.paused {
		t.pos += now.Sub(t.last).Seconds()
	}
	t.last = now
	t.clamp()
}

func (t *tracker) clamp() {
	if t.pos < 0 {
		t.pos = 0
	}
	if t.dur > 0 && t.pos > t.dur {
		t.pos = t.dur
	}
}

func (t *tracker) now() float64 {
	t.update()
	return t.pos
}

// skip leaves out time the device spent asleep.
func (t *tracker) skip(gap time.Duration) { t.last = t.last.Add(gap) }

// key mirrors what Onion's FFplay does with each button.
func (t *tracker) key(k int) {
	if k&keyRepeat != 0 {
		return // FFplay doesn't act on held buttons
	}
	t.update()
	switch k {
	case BtnLeft:
		t.pos -= 10
	case BtnRight:
		t.pos += 10
	case BtnDown:
		t.pos -= 60
	case BtnUp:
		t.pos += 60
	case BtnY:
		t.pos -= 600
	case BtnX:
		t.pos += 600
	case BtnA:
		t.paused = !t.paused
	case BtnB:
		t.paused = true // frame by frame leaves it paused
	}
	t.clamp()
}

// ---------- drawing ----------

func DrawDVR(c *Canvas, fonts *Fonts, d *DVR, now time.Time) {
	W := c.W
	for y := 0; y < c.H; y++ {
		c.Fill(0, y, W, 1, mix(rgb(0x06, 0x0c, 0x38), guideNavy, float64(y)/float64(c.H)))
	}
	c.Fill(0, 0, W, 64, rgb(0x04, 0x08, 0x2a))
	c.Fill(0, 62, W, 2, guideCyan)
	c.Text(fonts.Head(22), 18, 10, "RABBIT EARS", guideYellow)
	// red REC lamp + DVR
	c.Circle(26, 46, 6, rgb(0xff, 0x30, 0x30), true, 0)
	c.Text(fonts.Body(15), 38, 37, "DVR  ·  ON DEMAND", guideCyan)
	clock := now.Format("3:04")
	vt := fonts.VT(44)
	ap := fonts.VT(20)
	c.Text(vt, W-24-vt.Width(clock)-ap.Width("PM")-4, 6, clock, guideWhite)
	c.Text(ap, W-24-ap.Width("PM"), 28, now.Format("PM"), guideWhite)

	// breadcrumb
	var crumbs []string
	for _, l := range d.stack[1:] {
		crumbs = append(crumbs, strings.ToUpper(l.node.Name))
	}
	crumb := "ALL CHANNELS"
	if len(crumbs) > 0 {
		crumb = strings.Join(crumbs, "  ›  ")
	}
	c.Fill(0, 64, W, 30, rgb(0x0e, 0x22, 0x80))
	cf := fonts.Body(15)
	c.Text(cf, 18, 70, fitText(cf, crumb, W-150), guideYellow)
	l := d.top()
	if n := len(l.node.Kids); n > dvrRows {
		c.TextRight(cf, W-18, 70, fmt.Sprintf("%d OF %d", l.sel+1, n), guideDim)
	}

	rowH := 54
	y0 := 100
	titleF := fonts.Body(19)
	metaF := fonts.VT(22)
	for r := 0; r < dvrRows; r++ {
		i := l.top + r
		if i >= len(l.node.Kids) {
			break
		}
		n := l.node.Kids[i]
		y := y0 + r*rowH
		sel := i == l.sel
		box := guideBlue
		if sel {
			box = guideBlue2
		}
		c.Fill(8, y, W-16, rowH-6, box)
		x := 22
		if n.isFolder() {
			eps := n.episodes(nil)
			fresh := 0
			for _, it := range eps {
				if !d.mark(it).Done {
					fresh++
				}
			}
			drawTape(c, x, y+12, fresh > 0)
			x += 40
			meta := fmt.Sprintf("%d EPS", len(eps))
			if len(eps) == 1 {
				meta = "1 EP"
			}
			if fresh > 0 && fresh < len(eps) {
				meta += fmt.Sprintf(" · %d NEW", fresh)
			} else if fresh == 0 {
				meta += " · WATCHED"
			}
			mw := metaF.Width(meta)
			c.Text(titleF, x, y+13, fitText(titleF, strings.ToUpper(n.Name), W-x-mw-40), guideWhite)
			c.TextRight(metaF, W-22, y+13, meta, guideDim)
		} else {
			it := n.Item
			m := d.mark(it)
			col := guideWhite
			switch {
			case m.Done:
				col = guideDim
				drawCheck(c, x+4, y+16, guideDim)
			case m.Pos > 0:
				c.Circle(x+10, y+24, 7, guideYellow, false, 2)
			default:
				c.Circle(x+10, y+24, 7, guideCyan, true, 0)
			}
			x += 40
			meta := ""
			if it.Dur > 0 {
				meta = fmtDur(time.Duration(it.Dur * float64(time.Second)))
				if !m.Done && m.Pos > 0 {
					meta = fmtDur(time.Duration((it.Dur-m.Pos)*float64(time.Second))) + " LEFT"
				}
			}
			mw := metaF.Width(meta)
			name := n.Name
			if n.Parent == d.root {
				name = n.Name // a single-video channel
			}
			c.Text(titleF, x, y+9, fitText(titleF, name, W-x-mw-40), col)
			if meta != "" {
				c.TextRight(metaF, W-22, y+13, meta, guideDim)
			}
			if !m.Done && m.Pos > 0 && it.Dur > 0 {
				bw := W - x - mw - 60
				c.Fill(x, y+34, bw, 4, rgb(0x0a, 0x1a, 0x60))
				c.Fill(x, y+34, int(float64(bw)*clamp01(m.Pos/it.Dur)), 4, guideYellow)
			}
		}
		if sel {
			c.Border(6, y-2, W-12, rowH-2, 2, guideYellow)
		}
	}
	if len(l.node.Kids) == 0 {
		c.TextCenter(fonts.Body(17), W/2, 220, "NOTHING IN HERE YET", guideDim)
	}

	fy := c.H - 32
	c.Fill(0, fy-2, W, 2, guideCyan)
	c.Fill(0, fy, W, 32, rgb(0x04, 0x08, 0x2a))
	bf := fonts.Body(14)
	if d.message != "" {
		c.Text(bf, 18, fy+8, fitText(bf, strings.ToUpper(d.message), W-36), guideYellow)
		return
	}
	s := d.selected()
	x := 14
	if s != nil && s.isFolder() {
		x = c.Button(bf, x, fy+7, "A", "OPEN", guideWhite)
	} else {
		x = c.Button(bf, x, fy+7, "A", "PLAY", guideWhite)
		x = c.Button(bf, x+6, fy+7, "Y", "RESTART", guideWhite)
	}
	if len(d.stack) > 1 {
		x = c.Button(bf, x+6, fy+7, "B", "BACK", guideWhite)
	}
	x = c.Button(bf, x+6, fy+7, "X", "WATCHED", guideWhite)
	x = c.Button(bf, x+6, fy+7, "SELECT", "LIVE TV", guideWhite)
	c.Button(bf, x+6, fy+7, "START", "MENU", guideWhite)
}

// drawTape: a little VHS cassette for folders (label lit if there's
// something new inside).
func drawTape(c *Canvas, x, y int, fresh bool) {
	c.Fill(x, y, 30, 20, rgb(0x14, 0x14, 0x18))
	lab := guideDim
	if fresh {
		lab = guideCyan
	}
	c.Fill(x+4, y+3, 22, 6, lab)
	c.Circle(x+9, y+14, 3, rgb(0x60, 0x60, 0x68), true, 0)
	c.Circle(x+21, y+14, 3, rgb(0x60, 0x60, 0x68), true, 0)
}

func drawCheck(c *Canvas, x, y int, col color.RGBA) {
	for i := 0; i < 5; i++ {
		c.Fill(x+i, y+6+i, 3, 3, col)
	}
	for i := 0; i < 9; i++ {
		c.Fill(x+5+i, y+10-i, 3, 3, col)
	}
}

// DrawUpNext: the card between episodes.
func DrawUpNext(c *Canvas, fonts *Fonts, title string) {
	c.Fill(0, 0, c.W, c.H, colBlack)
	c.TextCenter(fonts.VT(30), c.W/2, 170, "UP NEXT", guideCyan)
	f := fonts.Body(22)
	lines := f.Wrap(title, c.W-80)
	for i, ln := range lines {
		if i > 1 {
			break
		}
		c.TextCenter(f, c.W/2, 214+i*30, ln, guideWhite)
	}
	c.TextCenter(fonts.Body(14), c.W/2, 300, "B: BACK TO RECORDINGS", guideDim)
}
