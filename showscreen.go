package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// "Show on router": the panel draws its own pages on the device screen for a while.
//
// The stock screen app (devuiappd) is paused with SIGSTOP, so it neither draws nor reacts to touches, and the
// touch device is grabbed (EVIOCGRAB) so no taps queue up for it. devuiappd is registered with watchdogd, which
// reboots the router when an app misses its keep-alives (interval 60 s + 60 s padding), so keep-alives are sent
// for it while it is paused. All of that is owned by a separate failsafe process started first in its own
// session: it sends the keep-alives and, if the panel does not finish in time (crash, hang), restores the saved
// screen and resumes devuiappd by itself at the deadline.

const (
	lcdOnPath     = "/sys/devices/79a2000.lcd/on"
	touchDev      = "/dev/input/event2" // cyttsp5_mt
	powerKeyDev   = "/dev/input/event0" // qpnp_pon
	savedFrame    = "/tmp/mifi-panel-screen.saved"
	failsafeGrace = 15 * time.Second
	keepAliveStep = 10 * time.Second
	minShow       = 10 * time.Second
	maxShow       = 10 * time.Minute
	longPress     = 1500 * time.Millisecond
	evioCGrab     = 0x40044590 // _IOW('E', 0x90, int)
	evKey         = 1
	btnTouch      = 330
	keyPower      = 116
)

// watchdog names that belong to the stock screen app (from watchdog_cli get_app_list).
var devuiWatchdogNames = []string{"devuiappd", "devui_main_thread"}

// ScreenSession is what /api/screen/session reports.
type ScreenSession struct {
	Active  bool   `json:"active"`
	Page    int    `json:"page"`
	Pages   int    `json:"pages"`
	EndsTS  int64  `json:"ends_ts"`
	Message string `json:"message"`
	Reason  string `json:"reason,omitempty"` // why the last session ended
	Error   string `json:"error,omitempty"`
}

type Shower struct {
	cfg Config
	col *Collector

	mu    sync.Mutex
	state ScreenSession
	stop  chan struct{}
	demo  *Canvas // what the demo "screen" shows during a session (for the mirror)
}

func NewShower(cfg Config, col *Collector) *Shower { return &Shower{cfg: cfg, col: col} }

func (s *Shower) Session() ScreenSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *Shower) DemoFrame() *Canvas {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Active {
		return s.demo
	}
	return nil
}

func (s *Shower) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		close(s.stop)
		s.stop = nil
	}
}

// Start shows layout l, beginning with page, for d (clamped to 10 s .. 10 min).
func (s *Shower) Start(l Layout, page int, d time.Duration) error {
	if err := l.Validate(); err != nil {
		return err
	}
	d = max(minShow, min(maxShow, d))
	if page < 0 || page >= len(l.Pages) {
		page = 0
	}
	s.mu.Lock()
	if s.state.Active {
		s.mu.Unlock()
		return errors.New("the router screen is already showing the panel")
	}
	stop := make(chan struct{})
	s.stop = stop
	s.state = ScreenSession{Active: true, Page: page, Pages: len(l.Pages), EndsTS: time.Now().Add(d).UnixMilli(), Message: "Starting…"}
	s.mu.Unlock()

	go func() {
		var err error
		var why string
		if s.cfg.Demo {
			why, err = s.runDemo(l, page, d, stop)
		} else {
			why, err = s.runLive(l, page, d, stop)
		}
		log.Printf("show on router ended: %s", why)
		s.mu.Lock()
		s.state.Active, s.demo, s.state.Reason = false, nil, why
		if err != nil {
			s.state.Error = err.Error()
			s.state.Message = "Stopped with an error"
			log.Printf("show on router: %v", err)
		} else {
			s.state.Error, s.state.Message = "", "The stock screen is back"
		}
		s.mu.Unlock()
	}()
	return nil
}

func (s *Shower) setPage(p int, msg string) {
	s.mu.Lock()
	s.state.Page, s.state.Message = p, msg
	s.mu.Unlock()
}

// runDemo has no device: it renders the pages for the mirror and switches pages every 5 s.
func (s *Shower) runDemo(l Layout, page int, d time.Duration, stop chan struct{}) (string, error) {
	end := time.Now().Add(d)
	best := map[string]float64{}
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for i := 0; ; i++ {
		if i > 0 && i%5 == 0 {
			page = (page + 1) % len(l.Pages)
		}
		c := s.render(l, page, best)
		s.mu.Lock()
		s.demo = c
		s.mu.Unlock()
		s.setPage(page, "Showing on the router (demo)")
		select {
		case <-stop:
			return "stopped from the panel", nil
		case <-t.C:
		}
		if time.Now().After(end) {
			return "time is up", nil
		}
	}
}

func (s *Shower) render(l Layout, page int, best map[string]float64) *Canvas {
	d := s.col.ScreenData()
	for m := range metricDefs {
		if v := metricValue(m, d.St.Signal); v != nil {
			if b, ok := best[m]; !ok || *v > b {
				best[m] = *v
			}
		}
	}
	d.AimBest = best
	return RenderPage(l, page, d)
}

func (s *Shower) runLive(l Layout, page int, d time.Duration, stop chan struct{}) (why string, err error) {
	why = "failed to start"
	pid := findPid("devuiappd")
	if pid <= 0 {
		return why, errors.New("the stock screen app (devuiappd) is not running")
	}
	fb, err := openFB(s.cfg.FB)
	if err != nil {
		return why, err
	}
	defer fb.Close()
	saved := append([]byte(nil), fb.mem...)
	if err := os.WriteFile(savedFrame, saved, 0o600); err != nil {
		return why, fmt.Errorf("saving the current screen: %w", err)
	}
	lcdWas := readTrim(lcdOnPath)

	// 1. The failsafe first: from here on, whatever happens, devuiappd is resumed at the deadline.
	deadline := time.Now().Add(d + failsafeGrace)
	fs, err := startFailsafe(pid, deadline, lcdWas, s.cfg)
	if err != nil {
		os.Remove(savedFrame)
		return why, fmt.Errorf("starting the failsafe: %w", err)
	}
	var touch *os.File
	// finish puts everything back. restoreLCD: switch the screen off again if it was off before the show.
	// Not done when the show ends from the router itself (long press, power button): the person is looking
	// at it, so the stock screen stays lit and devuiappd turns it off later on its own timeout.
	finish := func(restoreLCD bool) {
		if touch != nil { // let go of the touch screen before devuiappd wakes up
			syscall.Syscall(syscall.SYS_IOCTL, touch.Fd(), evioCGrab, 0)
			touch.Close()
			touch = nil
		}
		copy(fb.mem, saved)
		_ = fb.update()
		if restoreLCD && lcdWas != "" && readTrim(lcdOnPath) != lcdWas {
			_ = os.WriteFile(lcdOnPath, []byte(lcdWas), 0o644)
		}
		_ = syscall.Kill(pid, syscall.SIGCONT)
		_ = fs.Signal(syscall.SIGUSR1) // done: the failsafe resumes devuiappd once more and exits
		_, _ = fs.Wait()
		os.Remove(savedFrame)
	}

	// 2. Grab the touch screen so taps reach only us; watch the power key without grabbing it.
	t, err := os.OpenFile(touchDev, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		finish(true)
		return why, fmt.Errorf("opening the touch screen: %w", err)
	}
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, t.Fd(), evioCGrab, 1); e != 0 {
		t.Close()
		finish(true)
		return why, fmt.Errorf("taking the touch screen: %v", e)
	}
	touch = t
	power, _ := os.OpenFile(powerKeyDev, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if power != nil {
		defer power.Close()
	}

	// 3. Screen on, stock app paused.
	if lcdWas == "0" {
		_ = os.WriteFile(lcdOnPath, []byte("1"), 0o644)
	}
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		finish(true)
		return why, fmt.Errorf("pausing devuiappd: %w", err)
	}
	defer func() {
		finish(why != "long press on the screen" && why != "power button")
	}()

	end := time.Now().Add(d)
	best := map[string]float64{}
	var g gesture
	draw := func() error {
		copy(fb.mem, s.render(l, page, best).RGB666())
		return fb.update()
	}
	if err := draw(); err != nil {
		return why, fmt.Errorf("drawing: %w", err)
	}
	s.setPage(page, "Showing on the router")
	lastDraw := time.Now()
	for {
		select {
		case <-stop:
			return "stopped from the panel", nil
		default:
		}
		now := time.Now()
		if now.After(end) {
			return "time is up", nil
		}
		act, src := readInput(touch, power, &g, now, 200*time.Millisecond)
		if act == actNone && g.held(time.Now()) {
			act, src = actExit, "long press on the screen"
		}
		switch act {
		case actExit:
			return src, nil
		case actNext:
			page = (page + 1) % len(l.Pages)
			s.setPage(page, "Showing on the router")
			lastDraw = time.Time{}
		}
		if time.Since(lastDraw) >= time.Second {
			if err := draw(); err != nil {
				return why, fmt.Errorf("drawing: %w", err)
			}
			lastDraw = time.Now()
		}
	}
}

// gesture turns touch events into taps and long presses.
type gesture struct {
	down   bool
	downAt time.Time
}

const (
	actNone = iota
	actNext
	actExit
)

func (g *gesture) touch(pressed bool, now time.Time) int {
	switch {
	case pressed && !g.down:
		g.down, g.downAt = true, now
	case !pressed && g.down:
		g.down = false
		if now.Sub(g.downAt) < longPress {
			return actNext
		}
		return actExit
	}
	return actNone
}

// held reports a long press while the finger is still on the screen.
func (g *gesture) held(now time.Time) bool { return g.down && now.Sub(g.downAt) >= longPress }

// inputEvent is struct input_event; its size follows the platform (16 bytes on ARMv7).
type inputEvent struct {
	Time  syscall.Timeval
	Type  uint16
	Code  uint16
	Value int32
}

// readInput waits up to d for touch or power key events and returns what they mean and what caused it.
func readInput(touch, power *os.File, g *gesture, now time.Time, d time.Duration) (int, string) {
	fds := []pollFd{{fd: int32(touch.Fd()), events: pollIn}}
	if power != nil {
		fds = append(fds, pollFd{fd: int32(power.Fd()), events: pollIn})
	}
	ts := syscall.NsecToTimespec(d.Nanoseconds())
	n, _, e := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&fds[0])), uintptr(len(fds)), uintptr(unsafe.Pointer(&ts)), 0, 0, 0)
	if e != 0 || n == 0 {
		return actNone, ""
	}
	var ev inputEvent
	buf := (*[unsafe.Sizeof(ev)]byte)(unsafe.Pointer(&ev))[:]
	act, src := actNone, ""
	for i, f := range []*os.File{touch, power} {
		if f == nil || i >= len(fds) || fds[i].revents&pollIn == 0 {
			continue
		}
		for {
			m, err := syscall.Read(int(f.Fd()), buf)
			if err != nil || m != len(buf) {
				break
			}
			if ev.Type != evKey {
				continue
			}
			switch {
			case f == touch && ev.Code == btnTouch:
				if a := g.touch(ev.Value != 0, time.Now()); a != actNone {
					act, src = a, "tap"
					if a == actExit {
						src = "long press on the screen"
					}
				}
			case f == power && ev.Code == keyPower && ev.Value == 1:
				act, src = actExit, "power button"
			}
		}
	}
	return act, src
}

func findPid(comm string) int {
	procs, _ := filepath.Glob("/proc/[0-9]*/comm")
	for _, p := range procs {
		if readTrim(p) == comm {
			pid, _ := strconv.Atoi(filepath.Base(filepath.Dir(p)))
			return pid
		}
	}
	return 0
}

// startFailsafe launches this binary again as a detached failsafe (own session, no terminal).
func startFailsafe(pid int, deadline time.Time, lcdWas string, cfg Config) (*os.Process, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	arg := fmt.Sprintf("%d,%d,%s", pid, deadline.Unix(), lcdWas)
	cmd := exec.Command(self, "-screen-failsafe", arg, "-fb", cfg.FB, "-vendor-dir", cfg.VendorDir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd.Process, nil
}

func parseFailsafeArg(s string) (pid int, deadline time.Time, lcdWas string, err error) {
	f := strings.Split(s, ",")
	if len(f) != 3 {
		return 0, time.Time{}, "", errors.New("want pid,deadline,lcd")
	}
	pid, err1 := strconv.Atoi(f[0])
	dl, err2 := strconv.ParseInt(f[1], 10, 64)
	if err1 != nil || err2 != nil || pid <= 1 {
		return 0, time.Time{}, "", errors.New("bad pid or deadline")
	}
	return pid, time.Unix(dl, 0), f[2], nil
}

// runScreenFailsafe keeps watchdogd happy for the paused devuiappd and resumes it no later than the deadline.
// SIGUSR1 from the panel means it finished cleanly; at the deadline (panel crashed or hung) the failsafe
// restores the saved screen itself first.
func runScreenFailsafe(arg string, cfg Config) {
	pid, deadline, lcdWas, err := parseFailsafeArg(arg)
	if err != nil {
		log.Fatal("screen failsafe: ", err)
	}
	if deadline.Sub(time.Now()) > maxShow+failsafeGrace+time.Minute {
		deadline = time.Now().Add(maxShow + failsafeGrace)
	}
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGUSR1, syscall.SIGTERM, syscall.SIGHUP)
	v := NewVendor(cfg.VendorDir)
	keepAlive := func() {
		if v == nil {
			return
		}
		for _, name := range devuiWatchdogNames {
			_, _ = v.run("watchdog_cli", "application_send_keep_alive", name+"\n", 5*time.Second)
		}
	}
	t := time.NewTicker(keepAliveStep)
	defer t.Stop()
	keepAlive()
	clean := false
loop:
	for {
		select {
		case sig := <-done:
			clean = sig == syscall.SIGUSR1
			break loop
		case <-t.C:
			if time.Now().After(deadline) {
				break loop
			}
			keepAlive()
		}
	}
	if !clean { // the panel did not finish: put the stock screen back ourselves
		if b, err := os.ReadFile(savedFrame); err == nil {
			if fb, err := openFB(cfg.FB); err == nil {
				copy(fb.mem, b)
				_ = fb.update()
				fb.Close()
			}
		}
		if lcdWas != "" {
			_ = os.WriteFile(lcdOnPath, []byte(lcdWas), 0o644)
		}
		os.Remove(savedFrame)
	}
	keepAlive()
	if readTrim(fmt.Sprintf("/proc/%d/comm", pid)) == "devuiappd" {
		_ = syscall.Kill(pid, syscall.SIGCONT)
	}
}
