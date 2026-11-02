package main

import (
	"testing"
	"time"
)

func TestGesture(t *testing.T) {
	var g gesture
	t0 := time.Unix(100, 0)
	if a := g.touch(true, t0); a != actNone {
		t.Fatal("press alone is no action")
	}
	if a := g.touch(false, t0.Add(200*time.Millisecond)); a != actNext {
		t.Fatalf("short tap must switch the page, got %d", a)
	}
	g.touch(true, t0)
	if g.held(t0.Add(time.Second)) {
		t.Fatal("1 s is not a long press yet")
	}
	if !g.held(t0.Add(longPress)) {
		t.Fatal("holding for 1.5 s is a long press")
	}
	if a := g.touch(false, t0.Add(2*time.Second)); a != actExit {
		t.Fatalf("release after a long press exits, got %d", a)
	}
	if a := g.touch(false, t0.Add(3*time.Second)); a != actNone {
		t.Fatal("a second release means nothing")
	}
}

func TestParseFailsafeArg(t *testing.T) {
	pid, dl, lcd, err := parseFailsafeArg("1079,1793640000,0")
	if err != nil || pid != 1079 || dl.Unix() != 1793640000 || lcd != "0" {
		t.Fatalf("%d %v %q %v", pid, dl, lcd, err)
	}
	for _, bad := range []string{"", "1079,x,1", "1,1793640000,1", "1079,1793640000"} {
		if _, _, _, err := parseFailsafeArg(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// In demo mode a session renders pages for the mirror, can be stopped, and refuses to start twice.
func TestShowerDemo(t *testing.T) {
	cfg := Config{Demo: true, Interval: time.Second, Radio: 2 * time.Second}
	col := NewCollector(cfg)
	col.sample()
	s := NewShower(cfg, col)
	if err := s.Start(DefaultLayout(), 1, time.Second); err != nil { // clamped up to 10 s
		t.Fatal(err)
	}
	if st := s.Session(); !st.Active || st.Page != 1 || st.Pages != 3 || st.EndsTS-time.Now().UnixMilli() < 9000 {
		t.Fatalf("session %+v", st)
	}
	if err := s.Start(DefaultLayout(), 0, time.Minute); err == nil {
		t.Fatal("a second session must be refused")
	}
	deadline := time.Now().Add(3 * time.Second)
	for s.DemoFrame() == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if s.DemoFrame() == nil {
		t.Fatal("no demo frame rendered")
	}
	s.Stop()
	for s.Session().Active && time.Now().Before(deadline.Add(2*time.Second)) {
		time.Sleep(20 * time.Millisecond)
	}
	if st := s.Session(); st.Active || st.Error != "" {
		t.Fatalf("after stop: %+v", st)
	}
	if s.DemoFrame() != nil {
		t.Fatal("the mirror must show the stock screen again")
	}
}
