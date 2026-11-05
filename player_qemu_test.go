package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Runs Onion's real ffplay (under qemu) with the exact arguments the app
// builds. RABBITEARS_FFPLAY=wrapper RABBITEARS_TESTVIDEO=file.mp4
func TestRealFFplay(t *testing.T) {
	bin, vid := os.Getenv("RABBITEARS_FFPLAY"), os.Getenv("RABBITEARS_TESTVIDEO")
	if bin == "" || vid == "" {
		t.Skip("needs RABBITEARS_FFPLAY and RABBITEARS_TESTVIDEO")
	}
	work := t.TempDir()
	p := NewPlayer(bin, work)
	p.Extra = "showinfo"
	p.LogLvl = "info"
	// a stale resume file must not win over our seek
	os.WriteFile(filepath.Join(work, "pos.cfg"), make([]byte, 67320), 0o644)
	ch := &Channel{Num: 4, Name: "Nature Hour"}
	osd := filepath.Join(work, "osd0.png")
	if err := writePNG(osd, RenderOSD(newFonts(), 320, 240, ch, nil, time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := p.Play(vid, 12.5, osd, 3); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Exited:
	case <-time.After(90 * time.Second):
		p.Stop()
		t.Fatal("ffplay hung")
	}
	b, _ := os.ReadFile(filepath.Join(work, "ffplay.log"))
	log := string(b)
	i := strings.Index(log, "n:0 ")
	if i < 0 {
		t.Fatalf("no frames reached the filter graph:\n%s", log)
	}
	first := log[i:]
	if j := strings.Index(first, "pts_time:"); j < 0 || !strings.HasPrefix(first[j+9:], "11.") && !strings.HasPrefix(first[j+9:], "12.") {
		t.Fatalf("seek not honoured: %s", first[:120])
	}
	t.Logf("first frame: %s", first[:100])
}
