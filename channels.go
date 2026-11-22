package main

// The channel lineup and the "live broadcast" schedule.
//
// Media/TV/<file>              → a channel that loops that video
// Media/TV/<folder>/...        → a channel that airs the videos inside
// Media/TV/<folder>/<show>/... → each show folder is one show; the channel
//                                rotates between shows one episode at a time
//                                (Show A ep 1, Show B ep 1, Show A ep 2 ...).
//                                Seasons inside a show stay in order.
// Media/TV/Commercials/...     → commercials played in breaks. A sub-folder
//                                named after a channel holds that channel's
//                                own commercials.
// A leading number ("07 MTV", "12 - Cartoons") picks the channel number.
// An order.txt in a channel folder (rotate, in order or shuffle) overrides
// the show order set in the menu for that channel.
//
// Every channel is always "on air": where you land when you tune in is worked
// out from the clock, so flipping away and back skips ahead the way real TV
// did. Commercial breaks are part of that timetable, so the guide's times
// stay right and every viewer sees the same break.

import (
	"encoding/binary"
	"encoding/json"
	"hash/fnv"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var videoExts = map[string]bool{
	".mp4": true, ".m4v": true, ".mov": true, ".mkv": true, ".avi": true, ".webm": true,
	".mpg": true, ".mpeg": true, ".ts": true, ".flv": true, ".wmv": true, ".3gp": true,
	".vob": true, ".m2ts": true,
}

// unknownDur is what a video we can't read the length of counts as in the
// schedule (it always plays from its start, with no breaks inside it).
const unknownDur = 30 * 60.0

// commercialsDir is the folder name (any case) that holds commercials.
const commercialsDir = "commercials"

// maxAdLen: longer videos in Commercials are skipped (someone dropped an
// episode in there by mistake).
const maxAdLen = 5 * 60.0

type Item struct {
	Path  string
	Rel   string // path inside its channel folder (for sorting and the DVR)
	Title string
	Show  string // show folder name, "" for loose videos
	Dur   float64
	W, H  int
}

func (it *Item) slot() float64 {
	if it.Dur > 0 {
		return it.Dur
	}
	return unknownDur
}

// Label is the title as shown in the guide: "Show - Episode" when the
// episode's own name doesn't already say which show it is.
func (it *Item) Label() string {
	if it.Show == "" || strings.Contains(norm(it.Title), norm(it.Show)) {
		return it.Title
	}
	return it.Show + " - " + it.Title
}

// Order of episodes on a channel.
const (
	OrderRotate     = "rotate"
	OrderSequential = "in order"
	OrderShuffle    = "shuffle"
)

func parseOrder(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "rotate", "rotation", "round robin", "mix":
		return OrderRotate
	case "in order", "sequential", "order", "chronological", "inorder", "in-order":
		return OrderSequential
	case "shuffle", "random", "randomize", "shuffled":
		return OrderShuffle
	}
	return ""
}

// Seg is one stretch of air time: part of an episode, or one commercial.
type Seg struct {
	Item   *Item
	From   float64 // where in the file it starts
	Len    float64 // how long it airs
	Start  float64 // offset in the channel's cycle
	Ad     bool
	Open   bool // length unknown: play the file to its end, no -t
	Block  int  // which episode block it belongs to
	LastAd bool // last commercial of its break
}

// Block is one episode with the break before it and the breaks inside it,
// which is what the guide lists.
type Block struct {
	Item  *Item
	Start float64
	Len   float64
	First int // first segment
}

type Channel struct {
	Num    int
	Name   string
	Key    string // name in the TV folder; stable across renumbering
	Dir    string // the channel folder ("" for a single-file channel)
	Order  string // the order this channel actually uses
	Items  []*Item
	Seed   float64
	Total  float64
	Segs   []Seg
	Blocks []Block
	Ads    []*Item // the commercials this channel draws from
}

type Airing struct {
	Item       *Item
	Start, End time.Time
}

// At returns which segment is on the air at t and how far into it.
func (c *Channel) At(t time.Time) (int, float64) {
	if len(c.Segs) == 0 || c.Total <= 0 {
		return 0, 0
	}
	now := float64(t.UnixNano())/1e9 + c.Seed
	p := now - c.Total*math.Floor(now/c.Total)
	i := sort.Search(len(c.Segs), func(i int) bool { return c.Segs[i].Start > p }) - 1
	if i < 0 {
		i = 0
	}
	off := p - c.Segs[i].Start
	if c.Segs[i].Open {
		off = 0
	}
	return i, off
}

// Upcoming lists the episode on now and the ones after it.
func (c *Channel) Upcoming(t time.Time, n int) []Airing {
	if len(c.Blocks) == 0 {
		return nil
	}
	si, off := c.At(t)
	seg := c.Segs[si]
	b := seg.Block
	into := seg.Start + off - c.Blocks[b].Start
	start := t.Add(-time.Duration(into * float64(time.Second)))
	var out []Airing
	for k := 0; k < n; k++ {
		bl := c.Blocks[b]
		end := start.Add(time.Duration(bl.Len * float64(time.Second)))
		out = append(out, Airing{Item: bl.Item, Start: start, End: end})
		start = end
		b = (b + 1) % len(c.Blocks)
	}
	return out
}

// AdBreakLeft: when segment si is a commercial, how long until the show
// comes back.
func (c *Channel) AdBreakLeft(si int, off float64) float64 {
	left := 0.0
	for i := si; i < len(c.Segs) && c.Segs[i].Ad; i++ {
		left += c.Segs[i].Len
	}
	return left - off
}

// ---------- the schedule ----------

type AdOptions struct {
	On       bool
	EveryMin float64 // mid-episode break interval; 0 = only between episodes
	PerBreak int
}

// breakPoints: where an episode of length d is interrupted. Never in the
// first or last few minutes.
func breakPoints(d, every float64) []float64 {
	if every <= 0 || d <= 0 {
		return nil
	}
	var out []float64
	for p := every; p <= d-3*60; p += every {
		out = append(out, p)
	}
	return out
}

func rng(seed uint64) func() uint64 {
	s := seed*0x9E3779B97F4A7C15 + 0x632BE59BD9B4E019
	return func() uint64 {
		s ^= s >> 12
		s ^= s << 25
		s ^= s >> 27
		return s * 2685821657736338717
	}
}

// Build lays out the channel's cycle: every episode in order, with a
// commercial break before it and at its break points.
func (c *Channel) Build(ads AdOptions) {
	c.Segs, c.Blocks = nil, nil
	pool := c.Ads
	useAds := ads.On && len(pool) > 0 && ads.PerBreak > 0
	nb := uint64(0)
	h := fnv.New64a()
	h.Write([]byte(c.Key))
	base := h.Sum64()
	addBreak := func(block int) {
		next := rng(base ^ (nb+1)*0xA24BAED4963EE407)
		nb++
		n := ads.PerBreak
		if n > len(pool) {
			n = len(pool)
		}
		used := map[int]bool{}
		for k := 0; k < n; k++ {
			j := int(next() % uint64(len(pool)))
			for used[j] {
				j = (j + 1) % len(pool)
			}
			used[j] = true
			c.Segs = append(c.Segs, Seg{Item: pool[j], Len: pool[j].Dur, Ad: true, Block: block, LastAd: k == n-1})
		}
	}
	for _, it := range c.Items {
		b := len(c.Blocks)
		c.Blocks = append(c.Blocks, Block{Item: it, First: len(c.Segs)})
		if useAds {
			addBreak(b)
		}
		if it.Dur <= 0 {
			c.Segs = append(c.Segs, Seg{Item: it, Len: unknownDur, Open: true, Block: b})
			continue
		}
		from := 0.0
		if useAds {
			for _, p := range breakPoints(it.Dur, ads.EveryMin*60) {
				c.Segs = append(c.Segs, Seg{Item: it, From: from, Len: p - from, Block: b})
				addBreak(b)
				from = p
			}
		}
		c.Segs = append(c.Segs, Seg{Item: it, From: from, Len: it.Dur - from, Block: b})
	}
	t := 0.0
	for i := range c.Segs {
		c.Segs[i].Start = t
		t += c.Segs[i].Len
	}
	for i := range c.Blocks {
		c.Blocks[i].Start = c.Segs[c.Blocks[i].First].Start
		end := t
		if i+1 < len(c.Blocks) {
			end = c.Segs[c.Blocks[i+1].First].Start
		}
		c.Blocks[i].Len = end - c.Blocks[i].Start
	}
	c.Total = t
}

// arrange puts a channel's videos in airing order.
func arrange(items []*Item, order string, key string) []*Item {
	sort.SliceStable(items, func(i, j int) bool { return natLess(items[i].Rel, items[j].Rel) })
	switch order {
	case OrderShuffle:
		h := fnv.New64a()
		h.Write([]byte(key))
		next := rng(h.Sum64())
		out := append([]*Item(nil), items...)
		for i := len(out) - 1; i > 0; i-- {
			j := int(next() % uint64(i+1))
			out[i], out[j] = out[j], out[i]
		}
		return out
	case OrderRotate:
		var groups [][]*Item
		idx := map[string]int{}
		for _, it := range items {
			g, ok := idx[it.Show]
			if !ok {
				g = len(groups)
				idx[it.Show] = g
				groups = append(groups, nil)
			}
			groups[g] = append(groups[g], it)
		}
		if len(groups) < 2 {
			return items
		}
		var out []*Item
		for k := 0; len(out) < len(items); k++ {
			for _, g := range groups {
				if k < len(g) {
					out = append(out, g[k])
				}
			}
		}
		return out
	}
	return items
}

// ---------- scanning ----------

var numPrefix = regexp.MustCompile(`^\s*(\d{1,3})(?:\s*[-_.:)]\s*|\s+)(.+)$`)

func cleanTitle(name string) string {
	name = strings.TrimSuffix(name, filepath.Ext(name))
	name = strings.ReplaceAll(name, "_", " ")
	if !strings.Contains(name, " ") {
		name = strings.ReplaceAll(name, ".", " ")
	}
	return strings.Join(strings.Fields(name), " ")
}

func cleanFolder(name string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(name, "_", " ")), " ")
}

func splitNumber(name string) (int, string) {
	if m := numPrefix.FindStringSubmatch(name); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 && n < 1000 {
			return n, m[2]
		}
	}
	return 0, name
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// norm makes names comparable: "07 - G4_TV" and "g4 tv" both become "g4tv".
func norm(s string) string {
	_, s = splitNumber(s)
	s = strings.TrimSuffix(s, filepath.Ext(s))
	return nonAlnum.ReplaceAllString(strings.ToLower(s), "")
}

// collect lists the videos under dir (any depth) with their show folder.
func collect(dir string, cache *durCache) []*Item {
	var items []*Item
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") && p != dir {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !isVideo(d.Name()) {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		it := &Item{Path: p, Rel: rel}
		if i := strings.IndexByte(rel, '/'); i > 0 {
			_, show := splitNumber(rel[:i])
			it.Show = cleanFolder(show)
		}
		_, t := splitNumber(filepath.Base(p))
		it.Title = cleanTitle(t)
		it.Dur, it.W, it.H = cache.probe(p)
		items = append(items, it)
		return nil
	})
	return items
}

type ScanOptions struct {
	First int
	Order string // default order; order.txt in a channel folder wins
}

// Scan builds the lineup from the TV folder, with each channel's commercials
// attached (call Build to lay out the schedule).
func Scan(dir string, opt ScanOptions, cache *durCache) []*Channel {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	if opt.Order == "" {
		opt.Order = OrderRotate
	}
	var chans []*Channel
	var adsGeneral []*Item
	adsFor := map[string][]*Item{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(dir, name)
		if e.IsDir() && strings.EqualFold(name, commercialsDir) {
			for _, ad := range collect(full, cache) {
				if ad.Dur <= 0 || ad.Dur > maxAdLen {
					continue
				}
				if ad.Show != "" {
					k := norm(strings.SplitN(ad.Rel, "/", 2)[0])
					adsFor[k] = append(adsFor[k], ad)
				}
				adsGeneral = append(adsGeneral, ad)
			}
			continue
		}
		ch := &Channel{Key: name, Order: opt.Order}
		var label string
		if e.IsDir() {
			ch.Num, label = splitNumber(name)
			ch.Name = cleanFolder(label)
			ch.Dir = full
			ch.Items = collect(full, cache)
			if b, err := os.ReadFile(filepath.Join(full, "order.txt")); err == nil {
				if o := parseOrder(string(b)); o != "" {
					ch.Order = o
				}
			}
		} else if isVideo(name) {
			ch.Num, label = splitNumber(name)
			ch.Name = cleanTitle(label)
			it := &Item{Path: full, Rel: name, Title: ch.Name}
			it.Dur, it.W, it.H = cache.probe(full)
			ch.Items = []*Item{it}
		} else {
			continue
		}
		if len(ch.Items) == 0 {
			continue
		}
		ch.Items = arrange(ch.Items, ch.Order, ch.Key)
		h := fnv.New32a()
		h.Write([]byte(ch.Key))
		ch.Seed = float64(h.Sum32() % 86400)
		chans = append(chans, ch)
	}
	cache.save()

	// commercials: a channel's own folder if it has one, else the general
	// pool minus other channels' own commercials
	var shared []*Item
	owned := map[string]bool{}
	for _, c := range chans {
		owned[norm(c.Key)] = true
		owned[norm(c.Name)] = true
	}
	for _, ad := range adsGeneral {
		if ad.Show != "" && owned[norm(strings.SplitN(ad.Rel, "/", 2)[0])] {
			continue
		}
		shared = append(shared, ad)
	}
	sort.SliceStable(shared, func(i, j int) bool { return natLess(shared[i].Rel, shared[j].Rel) })
	for _, c := range chans {
		own := adsFor[norm(c.Key)]
		if len(own) == 0 {
			own = adsFor[norm(c.Name)]
		}
		if len(own) > 0 {
			c.Ads = own
		} else {
			c.Ads = shared
		}
	}

	sort.SliceStable(chans, func(i, j int) bool { return natLess(chans[i].Key, chans[j].Key) })
	taken := map[int]bool{}
	for _, c := range chans {
		if c.Num > 0 {
			if taken[c.Num] {
				c.Num = 0
			} else {
				taken[c.Num] = true
			}
		}
	}
	n := opt.First
	for _, c := range chans {
		if c.Num == 0 {
			for taken[n] {
				n++
			}
			c.Num = n
			taken[n] = true
		}
	}
	sort.SliceStable(chans, func(i, j int) bool { return chans[i].Num < chans[j].Num })
	return chans
}

type durCache struct {
	path  string
	m     map[string]cacheEntry
	dirty bool
}

type cacheEntry struct {
	Size  int64   `json:"s"`
	MTime int64   `json:"m"`
	Dur   float64 `json:"d"`
	W     int     `json:"w"`
	H     int     `json:"h"`
}

func loadCache(path string) *durCache {
	c := &durCache{path: path, m: map[string]cacheEntry{}}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &c.m)
	}
	return c
}

func (c *durCache) save() {
	if !c.dirty || c.path == "" {
		return
	}
	if b, err := json.Marshal(c.m); err == nil {
		os.WriteFile(c.path, b, 0o644)
	}
}

func (c *durCache) probe(path string) (float64, int, int) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, 0, 0
	}
	if e, ok := c.m[path]; ok && e.Size == st.Size() && e.MTime == st.ModTime().Unix() {
		return e.Dur, e.W, e.H
	}
	d, w, h := probeMP4(path)
	c.m[path] = cacheEntry{st.Size(), st.ModTime().Unix(), d, w, h}
	c.dirty = true
	return d, w, h
}

func isVideo(name string) bool {
	if strings.HasSuffix(name, ".part.mp4") {
		return false
	}
	return videoExts[strings.ToLower(filepath.Ext(name))]
}

// natLess sorts "Episode 2" before "Episode 10".
func natLess(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	for a != "" && b != "" {
		ra, rb := rune(a[0]), rune(b[0])
		if unicode.IsDigit(ra) && unicode.IsDigit(rb) {
			i, j := 0, 0
			for i < len(a) && unicode.IsDigit(rune(a[i])) {
				i++
			}
			for j < len(b) && unicode.IsDigit(rune(b[j])) {
				j++
			}
			na, _ := strconv.ParseUint(strings.TrimLeft(a[:i], "0")+"0", 10, 64)
			nb, _ := strconv.ParseUint(strings.TrimLeft(b[:j], "0")+"0", 10, 64)
			if na != nb {
				return na < nb
			}
			a, b = a[i:], b[j:]
			continue
		}
		if ra != rb {
			return ra < rb
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

// ---------- MP4 header reading ----------

// probeMP4 reads the duration and picture size from an MP4/MOV's moov box
// without decoding anything. Returns zeros for other formats.
func probeMP4(path string) (float64, int, int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, 0
	}
	defer f.Close()
	st, _ := f.Stat()
	size := st.Size()
	var pos int64
	hdr := make([]byte, 16)
	for pos+8 <= size {
		if _, err := f.ReadAt(hdr[:8], pos); err != nil {
			return 0, 0, 0
		}
		bl := int64(binary.BigEndian.Uint32(hdr[:4]))
		typ := string(hdr[4:8])
		hl := int64(8)
		if bl == 1 {
			if _, err := f.ReadAt(hdr[8:16], pos+8); err != nil {
				return 0, 0, 0
			}
			bl = int64(binary.BigEndian.Uint64(hdr[8:16]))
			hl = 16
		} else if bl == 0 {
			bl = size - pos
		}
		if bl < hl {
			return 0, 0, 0
		}
		if typ == "moov" {
			n := bl - hl
			if n > 32<<20 {
				return 0, 0, 0
			}
			buf := make([]byte, n)
			if _, err := io.ReadFull(io.NewSectionReader(f, pos+hl, n), buf); err != nil {
				return 0, 0, 0
			}
			return parseMoov(buf)
		}
		pos += bl
	}
	return 0, 0, 0
}

func parseMoov(b []byte) (dur float64, w, h int) {
	walkBoxes(b, func(typ string, body []byte) bool {
		switch typ {
		case "mvhd":
			if len(body) < 20 {
				return false
			}
			if body[0] == 1 && len(body) >= 32 {
				ts := binary.BigEndian.Uint32(body[20:])
				d := binary.BigEndian.Uint64(body[24:])
				if ts > 0 {
					dur = float64(d) / float64(ts)
				}
			} else {
				ts := binary.BigEndian.Uint32(body[12:])
				d := binary.BigEndian.Uint32(body[16:])
				if ts > 0 && d != 0xffffffff {
					dur = float64(d) / float64(ts)
				}
			}
		case "trak":
			return true
		case "tkhd":
			if w == 0 && len(body) >= 84 {
				tw := int(binary.BigEndian.Uint32(body[len(body)-8:]) >> 16)
				th := int(binary.BigEndian.Uint32(body[len(body)-4:]) >> 16)
				if tw > 0 && th > 0 {
					w, h = tw, th
				}
			}
		}
		return false
	})
	return
}

// walkBoxes calls fn for each box; returning true descends into it.
func walkBoxes(b []byte, fn func(string, []byte) bool) {
	for len(b) >= 8 {
		l := int(binary.BigEndian.Uint32(b))
		hl := 8
		if l == 1 && len(b) >= 16 {
			l = int(binary.BigEndian.Uint64(b[8:]))
			hl = 16
		} else if l == 0 {
			l = len(b)
		}
		if l < hl || l > len(b) {
			return
		}
		if fn(string(b[4:8]), b[hl:l]) {
			walkBoxes(b[hl:l], fn)
		}
		b = b[l:]
	}
}
