package main

// Rabbit Ears — a 90s TV set for OnionOS on the Miyoo Mini / Mini Plus.
// Videos in SD:/Media/TV become channels; flip through them with the d-pad.

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const version = "1.0.0"

type Config struct {
	TVFolder     string  `json:"tv_folder"`     // where the channels live
	FirstChannel int     `json:"first_channel"` // number of the first unnumbered channel
	StaticMS     int     `json:"static_ms"`     // how long the snow shows between channels
	StaticSound  float64 `json:"static_sound"`  // seconds of snow + hiss as the new channel starts; 0 turns it off
	OSDSeconds   float64 `json:"osd_seconds"`   // how long the channel number stays up
	Live         bool    `json:"live"`          // true: channels are "on air"; false: start from the top
	Player       string  `json:"player"`        // ffplay binary
	ExtraFilters string  `json:"extra_filters"` // extra ffplay -vf filters, e.g. scanlines
	PowerOnLast  bool    `json:"power_on_last"` // come back on the channel you left
}

func defaultConfig() Config {
	return Config{
		TVFolder:     "/mnt/SDCARD/Media/TV",
		FirstChannel: 2,
		StaticMS:     350,
		StaticSound:  0.5,
		OSDSeconds:   3,
		Live:         true,
		Player:       "/mnt/SDCARD/.tmp_update/bin/ffplay",
		PowerOnLast:  true,
	}
}

func loadConfig(path string) (Config, string) {
	cfg := defaultConfig()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if out, err := json.MarshalIndent(cfg, "", "  "); err == nil {
				os.WriteFile(path, append(out, '\n'), 0o644)
			}
		}
		return cfg, ""
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return defaultConfig(), fmt.Sprintf("config.json: %v", err)
	}
	if cfg.FirstChannel < 1 {
		cfg.FirstChannel = 2
	}
	if cfg.StaticMS < 0 {
		cfg.StaticMS = 0
	}
	if cfg.OSDSeconds <= 0 {
		cfg.OSDSeconds = 3
	}
	return cfg, ""
}

type State struct {
	Channel string `json:"channel"` // key of the last channel watched
}

func main() {
	shot := flag.String("shot", "", "render a demo screen to shot.png: osd|info|guide|bars|nosignal|snow|on|off")
	icon := flag.String("icon", "", "write the app icon PNG to this path")
	scan := flag.String("scan", "", "print the lineup for a TV folder and exit")
	flag.Parse()

	if *icon != "" {
		if err := writePNG(*icon, appIcon(110)); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		return
	}
	if *scan != "" {
		printLineup(*scan)
		return
	}
	if *shot != "" {
		if err := renderShot(*shot); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		return
	}

	exe, _ := os.Executable()
	appDir := filepath.Dir(exe)
	dataDir := filepath.Join(appDir, "data")
	os.MkdirAll(dataDir, 0o755)
	if lf, err := os.OpenFile(filepath.Join(dataDir, "rabbitears.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
		log.SetOutput(lf)
	}
	log.Printf("Rabbit Ears %s", version)
	cfg, problem := loadConfig(filepath.Join(dataDir, "config.json"))

	fb, err := OpenFramebuffer()
	if err != nil {
		log.Fatalf("framebuffer: %v", err)
	}
	defer fb.Close()

	tv := &TV{
		cfg:     cfg,
		dataDir: dataDir,
		fb:      fb,
		canvas:  NewCanvas(fb.W, fb.H),
		fonts:   newFonts(),
		keys:    make(chan int, 16),
		player:  NewPlayer(cfg.Player, "/tmp/rabbitears"),
		problem: problem,
	}
	tv.player.Extra = cfg.ExtraFilters
	tv.player.Static = cfg.StaticSound
	tv.player.NoFlip = os.Getenv("RABBITEARS_NOFLIP") != ""
	input := "/dev/input/event0"
	if d := os.Getenv("RABBITEARS_INPUT"); d != "" {
		input = d
	}
	if err := ReadInput(input, tv.keys); err != nil {
		log.Fatalf("input: %v", err)
	}
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		<-sig
		tv.player.Stop()
		os.Exit(0)
	}()
	tv.Run()
}

func printLineup(dir string) {
	chans := Scan(dir, defaultConfig().FirstChannel, &durCache{m: map[string]cacheEntry{}})
	if len(chans) == 0 {
		fmt.Println("no channels found in", dir)
		return
	}
	now := time.Now()
	for _, c := range chans {
		i, off := c.At(now)
		it := c.Items[i]
		fmt.Printf("CH %-3d %-28s %3d video(s)  now: %s @ %s", c.Num, c.Name, len(c.Items), it.Title, time.Duration(off*float64(time.Second)).Round(time.Second))
		if it.Dur <= 0 {
			fmt.Print("  (length unknown — not an MP4?)")
		} else if it.H > 480 {
			fmt.Printf("  (%dx%d — run it through vhsify)", it.W, it.H)
		}
		fmt.Println()
	}
}

// ---------- the TV ----------

type TV struct {
	cfg     Config
	dataDir string
	fb      *Framebuffer
	canvas  *Canvas
	fonts   *Fonts
	keys    chan int
	player  *Player
	problem string

	chans    []*Channel
	cur      int
	prev     int
	curItem  int
	snow     Snow
	fails    int
	lastTick time.Time
	mode     int // modeWatch, modeGuide, modeNoSignal, modeEmpty
	guide    Guide
	why      string
	pending  []int
}

const (
	modeWatch = iota
	modeGuide
	modeNoSignal
	modeEmpty
)

func (tv *TV) present() {
	tv.fb.Reset()
	tv.fb.Present(tv.canvas)
}

func (tv *TV) rescan() {
	cache := loadCache(filepath.Join(tv.dataDir, "lengths.json"))
	tv.chans = Scan(tv.cfg.TVFolder, tv.cfg.FirstChannel, cache)
	log.Printf("%d channels in %s", len(tv.chans), tv.cfg.TVFolder)
}

func (tv *TV) Run() {
	os.MkdirAll(tv.cfg.TVFolder, 0o755)
	os.WriteFile("/tmp/stay_awake", nil, 0o644)
	defer os.Remove("/tmp/stay_awake")
	os.WriteFile("/sys/devices/system/cpu/cpu0/cpufreq/scaling_governor", []byte("performance"), 0o644)

	tv.rescan()
	if tv.cfg.PowerOnLast {
		var st State
		if b, err := os.ReadFile(filepath.Join(tv.dataDir, "state.json")); err == nil && json.Unmarshal(b, &st) == nil {
			for i, c := range tv.chans {
				if c.Key == st.Channel {
					tv.cur = i
				}
			}
		}
	}
	tv.prev = tv.cur

	tv.powerOn()
	if _, err := os.Stat(tv.cfg.Player); err != nil {
		tv.showEmpty("PLAYER MISSING", "Onion's video player wasn't found at", tv.cfg.Player)
	} else if tv.problem != "" {
		tv.showEmpty("CONFIG ERROR", tv.problem, "Fix App/RabbitEars/data/config.json")
	} else if len(tv.chans) == 0 {
		tv.showEmpty("NO CHANNELS", "Put videos (or folders of episodes) in", "SD card: Media/TV  —  then press A")
	} else {
		tv.tune(tv.cur, true, false)
	}

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	tv.lastTick = time.Now()
	for {
		if len(tv.pending) > 0 {
			k := tv.pending[0]
			tv.pending = tv.pending[1:]
			if tv.handleKey(k) {
				tv.powerOff()
				return
			}
			continue
		}
		select {
		case k := <-tv.keys:
			if tv.handleKey(k) {
				tv.powerOff()
				return
			}
		case ev := <-tv.player.Exited:
			if !tv.player.Current(ev) {
				continue
			}
			if tv.menuPending(250 * time.Millisecond) {
				// ffplay quits on MENU by itself; that's our cue too
				tv.powerOff()
				return
			}
			tv.onVideoEnded(ev)
		case now := <-tick.C:
			// the whole app was paused (Onion sleep) → back on schedule
			if now.Sub(tv.lastTick) > 4*time.Second && tv.mode == modeWatch && tv.cfg.Live {
				log.Printf("woke after %v; retuning", now.Sub(tv.lastTick).Round(time.Second))
				tv.tune(tv.cur, true, false)
			}
			tv.lastTick = time.Now()
			if tv.mode == modeGuide {
				tv.drawGuide()
			}
		}
	}
}

// menuPending waits briefly for a MENU press that may be racing ffplay's exit.
func (tv *TV) menuPending(d time.Duration) bool {
	deadline := time.After(d)
	for {
		select {
		case k := <-tv.keys:
			if k == BtnMenu {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func (tv *TV) saveState() {
	if tv.cur < len(tv.chans) {
		if b, err := json.Marshal(State{Channel: tv.chans[tv.cur].Key}); err == nil {
			os.WriteFile(filepath.Join(tv.dataDir, "state.json"), b, 0o644)
		}
	}
}

// handleKey returns true when it's time to switch off.
func (tv *TV) handleKey(k int) bool {
	if k == BtnMenu {
		return true
	}
	switch tv.mode {
	case modeEmpty:
		if k == BtnA || k == BtnStart {
			tv.rescan()
			if len(tv.chans) > 0 && tv.problem == "" {
				tv.cur = 0
				tv.tune(0, true, false)
			}
		}
	case modeGuide:
		switch k {
		case BtnUp:
			tv.guide.Sel = (tv.guide.Sel - 1 + len(tv.chans)) % len(tv.chans)
		case BtnDown:
			tv.guide.Sel = (tv.guide.Sel + 1) % len(tv.chans)
		case BtnL1, BtnLeft:
			tv.guide.Sel = max(0, tv.guide.Sel-guideRows)
		case BtnR1, BtnRight:
			tv.guide.Sel = min(len(tv.chans)-1, tv.guide.Sel+guideRows)
		case BtnA, BtnStart:
			tv.switchTo(tv.guide.Sel)
			return false
		case BtnB, BtnSelect:
			tv.tune(tv.cur, true, false)
			return false
		}
		tv.drawGuide()
	default: // watching or no signal
		switch k {
		case BtnUp, BtnR1, BtnRight, BtnDown, BtnL1, BtnLeft:
			// fold a held or mashed button into one jump so surfing stays snappy
			n := len(tv.chans)
			step := tv.collectSteps(chanStep(k))
			tv.switchTo(((tv.cur+step)%n + n) % n)
		case BtnB:
			tv.switchTo(tv.prev) // "recall": the last channel you were on
		case BtnSelect, BtnStart:
			tv.openGuide()
		case BtnA, BtnX, BtnY:
			tv.tune(tv.cur, false, true) // DISPLAY
		case BtnL2, BtnR2:
			// ffplay ignores these; nothing to undo
		}
	}
	return false
}

func chanStep(k int) int {
	switch k {
	case BtnUp, BtnR1, BtnRight:
		return 1
	case BtnDown, BtnL1, BtnLeft:
		return -1
	}
	return 0
}

// collectSteps adds up channel presses already waiting in the queue; the
// first other button stops it and is handled afterwards.
func (tv *TV) collectSteps(step int) int {
	for {
		select {
		case k := <-tv.keys:
			if s := chanStep(k); s != 0 {
				step += s
				continue
			}
			tv.pending = append(tv.pending, k)
		default:
		}
		return step
	}
}

func (tv *TV) switchTo(i int) {
	if len(tv.chans) == 0 {
		return
	}
	if i != tv.cur {
		tv.prev = tv.cur
	}
	tv.cur = i
	tv.fails = 0
	tv.tune(i, true, false)
	tv.saveState()
}

// tune starts channel i at whatever is "on air" now.
func (tv *TV) tune(i int, withSnow, withInfo bool) {
	if len(tv.chans) == 0 {
		return
	}
	tv.player.Stop()
	tv.mode = modeWatch
	ch := tv.chans[i]
	idx, off := 0, 0.0
	if tv.cfg.Live {
		idx, off = ch.At(time.Now())
		// too close to the end to be worth starting: go to what's next
		if it := ch.Items[idx]; it.Dur > 0 && it.Dur-off < 3 {
			idx, off = (idx+1)%len(ch.Items), 0
		}
	}
	tv.start(ch, idx, off, withSnow, true, withInfo)
}

// start launches one item, showing snow (while ffplay loads) and the OSD.
func (tv *TV) start(ch *Channel, idx int, off float64, withSnow, withOSD, withInfo bool) {
	tv.curItem = idx
	it := ch.Items[idx]
	osd := ""
	if withOSD {
		w, h := it.W, it.H
		if w <= 0 || h <= 0 {
			w, h = 320, 240
		}
		var info *Airing
		if withInfo {
			if air := ch.Upcoming(time.Now(), 1); len(air) > 0 && tv.cfg.Live {
				info = &air[0]
			} else {
				a := Airing{Item: it, Start: time.Now().Add(-time.Duration(off * float64(time.Second)))}
				a.End = a.Start.Add(time.Duration(it.slot() * float64(time.Second)))
				info = &a
			}
		}
		img := RenderOSD(tv.fonts, w, h, ch, info, time.Now())
		osd = filepath.Join(tv.player.Work, fmt.Sprintf("osd%d.png", time.Now().UnixNano()%2))
		if err := writePNG(osd, img); err != nil {
			log.Printf("osd: %v", err)
			osd = ""
		}
	}
	secs := tv.cfg.OSDSeconds
	if withInfo {
		secs += 2
	}
	if err := tv.player.Play(it.Path, off, osd, secs, withSnow); err != nil {
		tv.noSignal(ch, err.Error())
		return
	}
	if withSnow && tv.cfg.StaticMS > 0 {
		end := time.Now().Add(time.Duration(tv.cfg.StaticMS) * time.Millisecond)
		for time.Now().Before(end) {
			tv.snow.Draw(tv.canvas)
			tv.present()
			time.Sleep(33 * time.Millisecond)
		}
	} else if withSnow && tv.cfg.StaticSound > 0 {
		// no cover at all: leave the screen alone and let ffplay's own
		// static (picture and sound) be the channel change
	} else {
		tv.canvas.Fill(0, 0, tv.canvas.W, tv.canvas.H, colBlack)
		tv.present()
	}
}

func (tv *TV) onVideoEnded(ev exitEvent) {
	ch := tv.chans[tv.cur]
	ran := time.Since(ev.started)
	if ran < 2500*time.Millisecond {
		tv.fails++
		log.Printf("ffplay ended after %v (%v); failure %d", ran.Round(time.Millisecond), ev.err, tv.fails)
		if tv.fails >= 3 {
			why := "This video won't play. Convert it with vhsify."
			if b, err := os.ReadFile(filepath.Join(tv.player.Work, "ffplay.log")); err == nil && len(b) > 0 {
				lines := strings.Split(strings.TrimSpace(string(b)), "\n")
				why = lines[len(lines)-1]
			}
			tv.noSignal(ch, why)
			return
		}
	} else {
		tv.fails = 0
	}
	// the programme ended: on to the next one, no snow, no OSD
	next := (tv.curItem + 1) % len(ch.Items)
	off := 0.0
	if tv.cfg.Live {
		if idx, o := ch.At(time.Now()); idx == next && ch.Items[idx].Dur-o > 3 {
			off = o
		}
	}
	if tv.fails > 0 {
		next, off = tv.curItem, 0 // retry the same item from the top
	}
	tv.start(ch, next, off, false, false, false)
}

func (tv *TV) noSignal(ch *Channel, why string) {
	tv.player.Stop()
	tv.mode = modeNoSignal
	tv.why = why
	log.Printf("no signal on %d: %s", ch.Num, why)
	DrawNoSignal(tv.canvas, tv.fonts, &tv.snow, ch, why)
	tv.present()
}

func (tv *TV) showEmpty(title, a, b string) {
	tv.player.Stop()
	tv.mode = modeEmpty
	DrawBars(tv.canvas, tv.fonts, title, a, b)
	tv.present()
}

func (tv *TV) openGuide() {
	tv.player.Stop()
	tv.mode = modeGuide
	tv.guide.Sel = tv.cur
	tv.drawGuide()
}

func (tv *TV) drawGuide() {
	DrawGuide(tv.canvas, tv.fonts, tv.chans, &tv.guide, time.Now(), tv.cur)
	tv.present()
}

func (tv *TV) powerOn() {
	under := NewCanvas(tv.canvas.W, tv.canvas.H)
	tv.snow.Draw(under)
	start := time.Now()
	for {
		t := time.Since(start).Seconds() / 0.45
		if t > 1 {
			break
		}
		PowerOnFrame(tv.canvas, under, t)
		tv.present()
		time.Sleep(16 * time.Millisecond)
	}
}

func (tv *TV) powerOff() {
	last := NewCanvas(tv.canvas.W, tv.canvas.H)
	if tv.mode == modeWatch {
		tv.fb.Capture(last)
	} else {
		copy(last.Pix, tv.canvas.Pix)
	}
	tv.player.Stop()
	tv.saveState()
	start := time.Now()
	for {
		t := time.Since(start).Seconds() / 0.55
		if t > 1 {
			break
		}
		PowerOffFrame(tv.canvas, last, t)
		tv.present()
		time.Sleep(16 * time.Millisecond)
	}
	tv.canvas.Fill(0, 0, tv.canvas.W, tv.canvas.H, colBlack)
	tv.present()
	time.Sleep(150 * time.Millisecond)
}
