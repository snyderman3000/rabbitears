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
	Extra   string // extra filters from config (applied before the flip)
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

// filterGraph builds -vf: optional OSD overlay for the first osdFrames frames
// of this ffplay, then the panel flip. Counted in frames from the start rather
// than by timestamp: ffplay backs up to the keyframe before -ss and plays from
// there. vhsify puts a keyframe every second, but other files can have them
// many seconds apart, and a timestamp window then made the channel number turn
// up late, by a different amount on every channel change.
func (p *Player) filterGraph(osd string, osdFrames int) string {
	var parts []string
	if osd != "" {
		parts = append(parts, fmt.Sprintf("movie=%s[osd];[in][osd]overlay=0:0:enable='lt(n,%d)'", ffEscape(osd), osdFrames))
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

// Play starts path at ss seconds with the OSD up for about osdSecs.
func (p *Player) Play(path string, ss float64, osd string, osdSecs float64) error {
	p.Stop()
	os.Remove(filepath.Join(p.Work, "pos.cfg"))
	args := []string{"-autoexit", "-loglevel", p.LogLvl}
	if ss > 0.05 {
		args = append(args, "-ss", fmt.Sprintf("%.2f", ss))
	}
	osdFrames := int(osdSecs*24 + 0.5) // counted at 24 fps: a little shorter on 30 fps video
	args = append(args, "-vf", p.filterGraph(osd, osdFrames), "-i", path)
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
