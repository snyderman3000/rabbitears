package main

// Plays one video at a time with Onion's own ffplay (FFmpeg 2.4, shipped in
// .tmp_update/bin). Things learned from running that exact binary:
//   * it keeps a resume table in ./pos.cfg and that overrides -ss, so we run
//     it in our own scratch folder and delete pos.cfg before every start;
//   * the panel is upside down, so the filter graph must end in hflip,vflip;
//   * there is no drawtext, but movie=+overlay with a timed enable works, so
//     the channel number is a PNG we draw ourselves.

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type exitEvent struct {
	gen     int
	started time.Time
	err     error
}

type Player struct {
	Bin     string
	Work    string
	NoFlip  bool
	Extra   string  // extra filters from config (applied before the flip)
	Static  float64 // seconds of snow + hiss at the start of a channel change; 0 = off
	LogLvl  string
	cmd     *exec.Cmd
	gen     int
	Exited  chan exitEvent
	running bool
}

func NewPlayer(bin, work string) *Player {
	os.MkdirAll(work, 0o755)
	return &Player{Bin: bin, Work: work, LogLvl: "error", Exited: make(chan exitEvent, 4)}
}

// filterGraph builds -vf: the optional tuning static and OSD overlay, then
// the panel flip. Both are timed in frames from the start of this ffplay (n),
// not by timestamp: ffplay backs up to the keyframe before -ss and plays from
// there, anywhere from a moment to several seconds early, so a timestamp
// window made the channel number turn up at a different moment every time.
func (p *Player) filterGraph(osd string, osdFrames, staticFrames int) string {
	var parts []string
	// the tuning static: the first frames ffplay plays are covered by snow made
	// to match Snow.Draw: 2x2 grain, mean ~100, the same spread, a touch of blue.
	// Counted from the start (not from the tune-in time): ffplay backs up to the
	// keyframe before -ss and plays from there, sound and all, so that is where
	// the static has to be. The branch ends after those frames, so the rest of
	// the show costs nothing.
	snow := ""
	if staticFrames > 0 {
		snow = fmt.Sprintf("[in]split[pic][fx];[fx]trim=end_frame=%d,scale=iw/2:ih/2:flags=neighbor,lutyuv=y=96:u=130:v=128,"+
			"noise=c0s=100:c0f=t+u,lutyuv=y='clip((val-96)*1.8+102,0,255)',scale=iw*2:ih*2:flags=neighbor[snow];"+
			"[pic][snow]overlay=0:0:eof_action=pass", staticFrames)
	}
	switch {
	case osd != "" && snow != "":
		parts = append(parts, fmt.Sprintf("%s[tv];movie=%s[osd];[tv][osd]overlay=0:0:enable='lt(n,%d)'", snow, ffEscape(osd), osdFrames))
	case osd != "":
		parts = append(parts, fmt.Sprintf("movie=%s[osd];[in][osd]overlay=0:0:enable='lt(n,%d)'", ffEscape(osd), osdFrames))
	case snow != "":
		parts = append(parts, snow)
	}
	if p.Extra != "" {
		parts = append(parts, p.Extra)
	}
	if !p.NoFlip {
		parts = append(parts, "hflip,vflip")
	}
	if len(parts) == 0 {
		return "null"
	}
	return strings.Join(parts, ",")
}

// ffEscape quotes a path for use inside a filter graph option.
func ffEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\\\`, `'`, `\\\'`, `:`, `\\:`, `,`, `\,`, `;`, `\;`, `[`, `\[`, `]`, `\]`)
	return r.Replace(s)
}

// staticAudio is the hiss under the snow. For the first p.Static seconds of
// playback (timed from the stream's own start, like the snow) the show's sound
// is off and a generated half-second of static plays instead; then the show's
// sound comes on. volume is evaluated once per audio frame, and only the
// static itself is computed sample by sample: an aeval over the show's audio
// cost ~27x the decoding for the whole programme and made tuning crawl.
// The static is softened noise (a one-pole low-pass in aevalsrc's variables
// 2 and 3), deeper than plain white noise, like a TV speaker. It's all in
// ffplay's own audio because Onion's audioserver takes one stream at a time
// (a second player would chop the first one up). amix halves its inputs while
// both run (hence 0.5) and drops to unity when the static ends.
func (p *Player) staticAudio() string {
	hiss := "0.5*st(2,0.9*ld(2)+0.1*(random(0)*2-1))|0.5*st(3,0.9*ld(3)+0.1*(random(1)*2-1))"
	// n is volume's own frame counter (one AAC frame = 1024 samples, ~21 ms),
	// which FFmpeg 2.4 always fills in, unlike startt
	return fmt.Sprintf("volume='gte(n,%d)':eval=frame[show];aevalsrc=exprs='%s':s=48000:d=%.2f[hiss];"+
		"[show][hiss]amix=inputs=2:duration=first:dropout_transition=0", int((p.Static-0.05)*48000/1024+0.5), hiss, p.Static)
}

// Play starts path at ss seconds; the OSD stays up for osdSecs (after the static, if any).
func (p *Player) Play(path string, ss float64, osd string, osdSecs float64, static bool) error {
	p.Stop()
	os.Remove(filepath.Join(p.Work, "pos.cfg"))
	args := []string{"-autoexit", "-loglevel", p.LogLvl}
	if ss > 0.05 {
		args = append(args, "-ss", fmt.Sprintf("%.2f", ss))
	}
	staticFrames := 0
	if static && p.Static > 0 {
		staticFrames = int(p.Static*24 + 0.5) // the shows are ~24 fps cartoons; at 30 fps it ends a touch earlier
	}
	osdFrames := staticFrames + int(osdSecs*24+0.5) // the shows are ~24 fps
	args = append(args, "-vf", p.filterGraph(osd, osdFrames, staticFrames))
	if staticFrames > 0 {
		args = append(args, "-af", p.staticAudio())
	}
	args = append(args, "-i", path)
	cmd := exec.Command(p.Bin, args...)
	cmd.Dir = p.Work
	cmd.Stdout = nil
	if lf, err := os.OpenFile(filepath.Join(p.Work, "ffplay.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
		cmd.Stderr = lf
		defer lf.Close()
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	p.gen++
	p.cmd = cmd
	p.running = true
	gen, started := p.gen, time.Now()
	log.Printf("play %q from %.1fs (pid %d)", path, ss, cmd.Process.Pid)
	go func() {
		err := cmd.Wait()
		p.Exited <- exitEvent{gen: gen, started: started, err: err}
	}()
	return nil
}

// Current reports whether an exit event belongs to the video playing now.
func (p *Player) Current(ev exitEvent) bool {
	if ev.gen == p.gen {
		p.running = false
		return true
	}
	return false
}

func (p *Player) Running() bool { return p.running }

// Stop ends playback: SIGTERM first so ffplay closes the audio device
// cleanly, then SIGKILL if it hangs.
func (p *Player) Stop() {
	if p.cmd == nil || !p.running {
		return
	}
	cmd := p.cmd
	p.gen++ // its exit event is now stale
	p.running = false
	pid := cmd.Process.Pid
	syscall.Kill(-pid, syscall.SIGTERM)
	deadline := time.Now().Add(800 * time.Millisecond)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil && strings.Contains(string(b), ") Z ") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	syscall.Kill(-pid, syscall.SIGKILL)
}
