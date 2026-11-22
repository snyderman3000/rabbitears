package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func eps(show string, n int, dur float64) []*Item {
	var out []*Item
	for i := 1; i <= n; i++ {
		out = append(out, &Item{Show: show, Title: show + " " + string(rune('0'+i)), Rel: show + "/Season 1/Ep " + string(rune('0'+i)) + ".mp4", Dur: dur})
	}
	return out
}

func titles(items []*Item) string {
	var s []string
	for _, it := range items {
		s = append(s, it.Title)
	}
	return strings.Join(s, ",")
}

func TestRotateShows(t *testing.T) {
	items := append(append(eps("A", 3, 60), eps("B", 1, 60)...), eps("C", 2, 60)...)
	got := titles(arrange(items, OrderRotate, "k"))
	if got != "A 1,B 1,C 1,A 2,C 2,A 3" {
		t.Fatal(got)
	}
	got = titles(arrange(items, OrderSequential, "k"))
	if got != "A 1,A 2,A 3,B 1,C 1,C 2" {
		t.Fatal(got)
	}
	sh := titles(arrange(items, OrderShuffle, "k"))
	if sh == got || len(strings.Split(sh, ",")) != 6 || sh != titles(arrange(items, OrderShuffle, "k")) {
		t.Fatal("shuffle should be a stable different order:", sh)
	}
}

func TestScheduleWithoutAds(t *testing.T) {
	c := &Channel{Items: []*Item{{Dur: 100}, {Dur: 50}, {Dur: 0}}}
	c.Build(AdOptions{})
	at := func(sec float64) (int, float64) { return c.At(time.Unix(0, int64(sec*1e9))) }
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

func TestScheduleWithBreaks(t *testing.T) {
	ads := []*Item{{Title: "ad1", Dur: 30}, {Title: "ad2", Dur: 15}, {Title: "ad3", Dur: 20}}
	ep := &Item{Title: "ep", Dur: 22 * 60}
	c := &Channel{Key: "x", Items: []*Item{ep, {Title: "ep2", Dur: 10 * 60}}, Ads: ads}
	c.Build(AdOptions{On: true, EveryMin: 8, PerBreak: 2})
	// ep: break, 0-8, break, 8-16, break, 16-22 ; ep2: break, 0-10 (no mid break: 8 > 10-3)
	var shape []string
	for _, s := range c.Segs {
		if s.Ad {
			shape = append(shape, "ad")
		} else {
			shape = append(shape, s.Item.Title)
		}
	}
	want := "ad,ad,ep,ad,ad,ep,ad,ad,ep,ad,ad,ep2"
	if strings.Join(shape, ",") != want {
		t.Fatalf("got %s", strings.Join(shape, ","))
	}
	if c.Segs[2].From != 0 || c.Segs[2].Len != 480 || c.Segs[5].From != 480 || c.Segs[8].From != 960 || c.Segs[8].Len != 22*60-960 {
		t.Fatalf("cut points wrong: %+v", c.Segs[5])
	}
	// no commercial twice in one break; the schedule is the same every build
	if c.Segs[0].Item == c.Segs[1].Item {
		t.Fatal("repeat in a break")
	}
	first := c.Segs[3].Item
	c.Build(AdOptions{On: true, EveryMin: 8, PerBreak: 2})
	if c.Segs[3].Item != first {
		t.Fatal("schedule not stable")
	}
	// the guide counts breaks as part of the episode
	if len(c.Blocks) != 2 || c.Blocks[1].Start != c.Segs[9].Start || c.Blocks[0].Len != c.Segs[9].Start {
		t.Fatal("blocks wrong")
	}
	si, off := 3, 10.0
	if left := c.AdBreakLeft(si, off); left != c.Segs[3].Len+c.Segs[4].Len-10 {
		t.Fatal("break left", left)
	}
	// off: no ads at all
	c.Build(AdOptions{On: false, EveryMin: 8, PerBreak: 2})
	if len(c.Segs) != 2 {
		t.Fatal("ads off still has breaks")
	}
	// between shows only
	c.Build(AdOptions{On: true, EveryMin: 0, PerBreak: 1})
	if len(c.Segs) != 4 || !c.Segs[0].Ad || c.Segs[1].Item != ep || c.Segs[1].Len != ep.Dur {
		t.Fatal("between-shows-only layout wrong")
	}
}

func TestTracker(t *testing.T) {
	tr := tracker{pos: 100, dur: 1000, last: time.Now().Add(-5 * time.Second)}
	tr.key(BtnRight)                  // +10 after 5s of play
	tr.key(BtnUp)                     // +60
	tr.key(BtnRight | keyRepeat)      // held: FFplay ignores it
	tr.key(BtnA)                      // pause
	tr.last = tr.last.Add(-time.Hour) // paused time doesn't count
	tr.key(BtnY)                      // -600 → clamps at 0
	if p := tr.now(); p != 0 {
		t.Fatal("clamp", p)
	}
	tr.key(BtnX) // +600
	if p := tr.now(); p < 599 || p > 601 {
		t.Fatal(p)
	}
	tr2 := tracker{pos: 100, dur: 1000, last: time.Now().Add(-5 * time.Second)}
	tr2.key(BtnRight)
	if p := tr2.now(); p < 114.9 || p > 115.2 {
		t.Fatal("seek", p)
	}
}

func makeVideo(t *testing.T, path string, secs int) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	cmd := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "color=c=blue:s=32x24:r=1", "-t", itoa(secs), "-c:v", "libx264", "-pix_fmt", "yuv420p", "-movflags", "+faststart", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
}

func itoa(n int) string {
	s := ""
	if n == 0 {
		return "0"
	}
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func TestScanLineup(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	dir := t.TempDir()
	makeVideo(t, filepath.Join(dir, "Cartoons", "Doug", "Season 1", "Ep 10.mp4"), 600)
	makeVideo(t, filepath.Join(dir, "Cartoons", "Doug", "Season 1", "Ep 2.mp4"), 600)
	makeVideo(t, filepath.Join(dir, "Cartoons", "Rugrats", "Ep 1.mp4"), 600)
	makeVideo(t, filepath.Join(dir, "09 Late Movie.mp4"), 900)
	makeVideo(t, filepath.Join(dir, "G4", "Attack of the Show.mp4"), 600)
	makeVideo(t, filepath.Join(dir, "Commercials", "Cereal.mp4"), 30)
	makeVideo(t, filepath.Join(dir, "Commercials", "Toys", "Ninja Turtles.mp4"), 15)
	makeVideo(t, filepath.Join(dir, "COMMERCIALS", "g4", "N64.mp4"), 20) // same folder on a case-insensitive card
	makeVideo(t, filepath.Join(dir, "Commercials", "Oops a whole episode.mp4"), 1200)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "G4", "order.txt"), []byte("Shuffle\n"), 0o644)

	chans := Scan(dir, ScanOptions{First: 2, Order: OrderRotate}, loadCache(filepath.Join(dir, ".cache.json")))
	var names []string
	for _, c := range chans {
		names = append(names, itoa(c.Num)+" "+c.Name)
	}
	if strings.Join(names, ",") != "2 Cartoons,3 G4,9 Late Movie" {
		t.Fatal("lineup:", names)
	}
	cart, g4, movie := chans[0], chans[1], chans[2]
	if got := titles(cart.Items); got != "Ep 2,Ep 1,Ep 10" {
		t.Fatal("rotation:", got)
	}
	if cart.Items[0].Label() != "Doug - Ep 2" {
		t.Fatal("label:", cart.Items[0].Label())
	}
	if g4.Order != OrderShuffle {
		t.Fatal("order.txt ignored")
	}
	if len(g4.Ads) != 1 || g4.Ads[0].Title != "N64" {
		t.Fatalf("G4 should get only its own commercials: %v", titles(g4.Ads))
	}
	if got := titles(cart.Ads); got != "Cereal,Ninja Turtles" || titles(movie.Ads) != got {
		t.Fatalf("shared pool: %s", got)
	}
	cart.Build(AdOptions{On: true, EveryMin: 8, PerBreak: 2})
	if !cart.Segs[0].Ad || cart.Segs[2].Item != cart.Items[0] || cart.Segs[2].Len != 600 || len(cart.Segs) != 9 {
		t.Fatal("schedule from scan wrong")
	}
}

func TestFilterGraph(t *testing.T) {
	p := &Player{}
	g := p.filterGraph("/tmp/rabbitears/osd1.png", 8, 13)
	want := "movie=/tmp/rabbitears/osd1.png[osd];[in][osd]overlay=0:0:enable='between(t,8.00,13.00)',hflip,vflip"
	if g != want {
		t.Fatalf("\n got %s\nwant %s", g, want)
	}
}

func TestDVRTreeAndNext(t *testing.T) {
	items := append(eps("Doug", 3, 600), eps("Rugrats", 2, 600)...)
	for _, it := range items {
		it.Path = "/tv/Cartoons/" + it.Rel
	}
	ch := &Channel{Num: 2, Name: "Cartoons", Key: "Cartoons", Dir: "/tv/Cartoons", Items: arrange(items, OrderRotate, "Cartoons")}
	single := &Channel{Num: 3, Name: "Movie", Key: "Movie.mp4", Items: []*Item{{Title: "Movie", Rel: "Movie.mp4", Path: "/tv/Movie.mp4", Dur: 5000}}}
	d := newDVR("")
	d.setLineup("/tv", []*Channel{ch, single})
	if len(d.root.Kids) != 2 || d.root.Kids[1].Item == nil {
		t.Fatal("root")
	}
	d.browserKey(BtnA) // into Cartoons
	if n := d.selected(); n == nil || n.Name != "Doug" {
		t.Fatal("first show")
	}
	d.browserKey(BtnA) // Doug
	d.browserKey(BtnA) // Season 1
	ep1 := d.selected()
	if ep1.Item == nil || ep1.Item.Title != "Doug 1" {
		t.Fatal("episode")
	}
	if nx := d.nextAfter(ep1); nx == nil || nx.Item.Title != "Doug 2" {
		t.Fatal("next should stay on the same show")
	}
	if d.nextAfter(d.nodeOf[items[2]]) != nil {
		t.Fatal("last Doug has no next")
	}
	// stop part-way, then resume a little before
	d.startPlaying(ep1, 0)
	d.track.pos = 250
	d.stopPlaying(false)
	if r := d.resumeAt(ep1.Item); r < 246.9 || r > 247.1 {
		t.Fatal("resume", r)
	}
	d.startPlaying(ep1, 247)
	d.stopPlaying(true)
	if !d.mark(ep1.Item).Done || d.resumeAt(ep1.Item) != 0 {
		t.Fatal("finished episode")
	}
	// X marks a whole folder, X again clears it
	d.browserKey(BtnB) // back to Doug
	d.browserKey(BtnX)
	for _, it := range items[:3] {
		if !d.mark(it).Done {
			t.Fatal("folder not marked")
		}
	}
	d.browserKey(BtnX)
	if d.mark(items[0]).Done {
		t.Fatal("folder not cleared")
	}
	// rebuilding the lineup keeps your place
	d.setLineup("/tv", []*Channel{ch, single})
	if d.top().node.Name != "Doug" {
		t.Fatal("lost place:", d.top().node.Name)
	}
}

func TestScaleInto(t *testing.T) {
	src := NewCanvas(640, 480)
	src.Fill(0, 0, 320, 480, rgb(255, 0, 0))
	src.Fill(320, 0, 320, 480, rgb(0, 0, 255))
	dst := NewCanvas(750, 560)
	scaleInto(dst, src)
	l := dst.Pix[(280*dst.Stride)+10*4:]
	r := dst.Pix[(280*dst.Stride)+740*4:]
	if l[0] != 255 || l[2] != 0 || r[2] != 255 || r[0] != 0 {
		t.Fatal("scaled wrong")
	}
}
