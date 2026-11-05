package main

// The channel lineup and the "live broadcast" schedule.
//
// Media/TV/<file>          → one channel that loops that video
// Media/TV/<folder>/...    → one channel that airs every video in the folder
//                            (sub-folders included) in order, then repeats
// A leading number ("07 MTV", "12 - Cartoons") picks the channel number.
//
// Every channel is always "on air": where you land when you tune in is worked
// out from the clock, so flipping away and back skips ahead the way real TV
// did, and every Rabbit Ears in the world shows the same thing at once.

import (
	"encoding/binary"
	"encoding/json"
	"hash/fnv"
	"io"
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
// schedule (it still always plays from its start).
const unknownDur = 30 * 60.0

type Item struct {
	Path  string
	Title string
	Dur   float64 // seconds; 0 = unknown
	W, H  int     // video size; 0 = unknown
}

func (it *Item) slot() float64 {
	if it.Dur > 0 {
		return it.Dur
	}
	return unknownDur
}

type Channel struct {
	Num   int
	Name  string
	Key   string // path relative to the TV folder; stable across renumbering
	Items []*Item
	Total float64
	Seed  float64
}

// At returns which item is on the air at t and how far into it.
func (c *Channel) At(t time.Time) (int, float64) {
	if len(c.Items) == 0 || c.Total <= 0 {
		return 0, 0
	}
	now := float64(t.UnixNano())/1e9 + c.Seed
	p := now - c.Total*float64(int64(now/c.Total))
	for i, it := range c.Items {
		if p < it.slot() {
			if it.Dur <= 0 {
				return i, 0
			}
			return i, p
		}
		p -= it.slot()
	}
	return len(c.Items) - 1, 0
}

// Next returns when item i (as scheduled at t) ends and what comes after it.
func (c *Channel) Upcoming(t time.Time, n int) []Airing {
	if len(c.Items) == 0 {
		return nil
	}
	i, off := c.At(t)
	start := t.Add(-time.Duration(off * float64(time.Second)))
	var out []Airing
	for k := 0; k < n; k++ {
		it := c.Items[i]
		end := start.Add(time.Duration(it.slot() * float64(time.Second)))
		out = append(out, Airing{Item: it, Start: start, End: end})
		start = end
		i = (i + 1) % len(c.Items)
	}
	return out
}

type Airing struct {
	Item       *Item
	Start, End time.Time
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

func splitNumber(name string) (int, string) {
	if m := numPrefix.FindStringSubmatch(name); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 && n < 1000 {
			return n, m[2]
		}
	}
	return 0, name
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

// Scan builds the lineup from the TV folder.
func Scan(dir string, first int, cache *durCache) []*Channel {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var chans []*Channel
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(dir, name)
		ch := &Channel{Key: name}
		var label string
		if e.IsDir() {
			ch.Num, label = splitNumber(name)
			filepath.WalkDir(full, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if strings.HasPrefix(d.Name(), ".") && p != full {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if !d.IsDir() && isVideo(d.Name()) {
					ch.Items = append(ch.Items, &Item{Path: p})
				}
				return nil
			})
			sort.Slice(ch.Items, func(i, j int) bool { return natLess(ch.Items[i].Path, ch.Items[j].Path) })
		} else if isVideo(name) {
			ch.Num, label = splitNumber(name)
			ch.Items = []*Item{{Path: full}}
		} else {
			continue
		}
		if len(ch.Items) == 0 {
			continue
		}
		ch.Name = cleanTitle(label)
		if e.IsDir() {
			ch.Name = strings.Join(strings.Fields(strings.ReplaceAll(label, "_", " ")), " ")
		}
		for _, it := range ch.Items {
			it.Dur, it.W, it.H = cache.probe(it.Path)
			_, t := splitNumber(filepath.Base(it.Path))
			it.Title = cleanTitle(t)
			if len(ch.Items) == 1 && !e.IsDir() {
				it.Title = ch.Name
			}
			ch.Total += it.slot()
		}
		h := fnv.New32a()
		h.Write([]byte(ch.Key))
		ch.Seed = float64(h.Sum32() % 86400)
		chans = append(chans, ch)
	}
	cache.save()
	sort.SliceStable(chans, func(i, j int) bool { return natLess(chans[i].Key, chans[j].Key) })

	// numbering: explicit numbers win; the rest count up from `first`
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
	n := first
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
	var cw, ch int
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
		case "trak", "mdia", "minf", "stbl":
			return true
		case "tkhd":
			if w == 0 && len(body) >= 84 {
				tw := int(binary.BigEndian.Uint32(body[len(body)-8:]) >> 16)
				th := int(binary.BigEndian.Uint32(body[len(body)-4:]) >> 16)
				if tw > 0 && th > 0 {
					w, h = tw, th
				}
			}
		case "stsd":
			// the coded picture size from the video sample entry. tkhd holds the
			// display size, which differs for non-square pixels (a 710x480 file
			// shown at 631x480): the OSD is overlaid on the coded frame, so it
			// must be drawn at the coded size or it lands short of the corner.
			if cw == 0 && len(body) >= 44 && videoEntries[string(body[12:16])] {
				cw = int(binary.BigEndian.Uint16(body[40:]))
				ch = int(binary.BigEndian.Uint16(body[42:]))
			}
		}
		return false
	})
	if cw > 0 && ch > 0 {
		w, h = cw, ch
	}
	return
}

// sample entry types whose stsd record has the visual layout (width/height at 32/34)
var videoEntries = map[string]bool{"avc1": true, "avc3": true, "hvc1": true, "hev1": true, "mp4v": true, "s263": true, "jpeg": true, "mjpa": true}

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
