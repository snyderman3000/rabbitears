package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNatLess(t *testing.T) {
	if !natLess("Episode 2.mp4", "Episode 10.mp4") || natLess("b", "a") || !natLess("s01e09", "s01e10") {
		t.Fatal("natural sort is wrong")
	}
}

func TestSplitNumber(t *testing.T) {
	cases := map[string]struct {
		n    int
		rest string
	}{
		"07 MTV":          {7, "MTV"},
		"12 - Cartoons":   {12, "Cartoons"},
		"3_Movies":        {3, "Movies"},
		"1984":            {0, "1984"},
		"2001 A Space...": {0, "2001 A Space..."},
		"Cartoons":        {0, "Cartoons"},
	}
	for in, want := range cases {
		n, rest := splitNumber(in)
		if n != want.n || rest != want.rest {
			t.Errorf("%q → %d %q, want %d %q", in, n, rest, want.n, want.rest)
		}
	}
}

func TestScheduleWalksItems(t *testing.T) {
	c := &Channel{Items: []*Item{{Dur: 100}, {Dur: 50}, {Dur: 0}}}
	for _, it := range c.Items {
		c.Total += it.slot()
	}
	at := func(sec float64) (int, float64) {
		return c.At(time.Unix(0, int64(sec*1e9)))
	}
	if i, o := at(10); i != 0 || o != 10 {
		t.Fatal(i, o)
	}
	if i, o := at(120); i != 1 || o != 20 {
		t.Fatal(i, o)
	}
	if i, o := at(160); i != 2 || o != 0 { // unknown length plays from its start
		t.Fatal(i, o)
	}
	if i, o := at(c.Total + 5); i != 0 || o != 5 { // and it loops
		t.Fatal(i, o)
	}
	up := c.Upcoming(time.Unix(120, 0), 3)
	if len(up) != 3 || up[0].Item != c.Items[1] || up[1].Item != c.Items[2] || up[2].Item != c.Items[0] {
		t.Fatal("upcoming order wrong")
	}
	if up[0].End.Unix() != 150 {
		t.Fatal("end time", up[0].End.Unix())
	}
}

// Uses real converter output if it's around (RABBITEARS_TESTVIDEO=path.mp4).
func TestProbeAndScan(t *testing.T) {
	src := os.Getenv("RABBITEARS_TESTVIDEO")
	if src == "" {
		t.Skip("set RABBITEARS_TESTVIDEO to a vhsify'd mp4")
	}
	d, w, h := probeMP4(src)
	if d < 1 || w != 320 || h != 240 {
		t.Fatalf("probe: %v %dx%d", d, w, h)
	}
	dir := t.TempDir()
	b, _ := os.ReadFile(src)
	os.MkdirAll(filepath.Join(dir, "Saturday_Cartoons", "Season 1"), 0o755)
	os.WriteFile(filepath.Join(dir, "Saturday_Cartoons", "Season 1", "Ep 10.mp4"), b, 0o644)
	os.WriteFile(filepath.Join(dir, "Saturday_Cartoons", "Season 1", "Ep 2.mp4"), b, 0o644)
	os.WriteFile(filepath.Join(dir, "09 Late Movie.mp4"), b, 0o644)
	os.WriteFile(filepath.Join(dir, "Big.Buck.Bunny.mp4"), b, 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "half.part.mp4"), b, 0o644)
	chans := Scan(dir, 2, loadCache(filepath.Join(dir, ".cache.json")))
	if len(chans) != 3 {
		t.Fatalf("want 3 channels, got %d", len(chans))
	}
	got := []string{}
	for _, c := range chans {
		got = append(got, c.Name)
	}
	if chans[0].Num != 2 || chans[0].Name != "Big Buck Bunny" || chans[1].Num != 3 || chans[1].Name != "Saturday Cartoons" || chans[2].Num != 9 || chans[2].Name != "Late Movie" {
		t.Fatalf("lineup: %v", got)
	}
	if chans[1].Items[0].Title != "Ep 2" || len(chans[1].Items) != 2 {
		t.Fatalf("playlist order: %s", chans[1].Items[0].Title)
	}
}

func TestFilterGraph(t *testing.T) {
	p := &Player{}
	g := p.filterGraph("/tmp/rabbitears/osd1.png", 120, 0)
	want := "movie=/tmp/rabbitears/osd1.png[osd];[in][osd]overlay=0:0:enable='lt(n,120)',hflip,vflip"
	if g != want {
		t.Fatalf("\n got %s\nwant %s", g, want)
	}
}
