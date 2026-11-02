package main

import (
	"testing"
	"time"
)

const iscSample = `# The format of this file is documented in the dhcpd.leases(5) manual page.
lease 192.168.1.5 {
  starts 4 1970/01/01 00:20:11;
  ends 4 1970/01/02 00:20:11;
  binding state active;
  next binding state free;
  hardware ethernet 3C:06:30:8D:41:AA;
  client-hostname "Pixel-7";
}
lease 192.168.1.2 {
  binding state active;
  hardware ethernet a4:83:e7:1c:22:9f;
}
lease 192.168.1.6 {
  binding state active;
  hardware ethernet 3c:06:30:8d:41:aa;
  client-hostname "Pixel-7";
}
`

func TestParseISCLeases(t *testing.T) {
	got := parseISCLeases(iscSample)
	if len(got) != 2 {
		t.Fatalf("want 2 clients, got %+v", got)
	}
	if got[0].MAC != "3c:06:30:8d:41:aa" || got[0].IP != "192.168.1.6" || got[0].Name != "Pixel-7" {
		t.Errorf("renewed lease should win: %+v", got[0])
	}
	if got[1].IP != "192.168.1.2" || got[1].Name != "" {
		t.Errorf("lease without hostname: %+v", got[1])
	}
}

func TestParseDnsmasqLeases(t *testing.T) {
	got := parseDnsmasqLeases("1700000000 A4:83:E7:1C:22:9F 192.168.1.2 laptop *\n1700000000 aa:bb:cc:dd:ee:ff 192.168.1.3 * *\n")
	if len(got) != 2 || got[0].Name != "laptop" || got[0].MAC != "a4:83:e7:1c:22:9f" || got[1].Name != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestHistoryClockJump(t *testing.T) {
	var h History
	iv := 2 * time.Second
	h.add(&Status{TS: 1_000_000}, iv)
	h.add(&Status{TS: 1_002_000}, iv)
	// The network sets the real time: the clock jumps from 1970 to 2026.
	h.add(&Status{TS: 1_793_600_000_000}, iv)
	if len(h.T) != 3 {
		t.Fatalf("len %d", len(h.T))
	}
	if h.T[2]-h.T[1] != 2000 || h.T[1]-h.T[0] != 2000 {
		t.Fatalf("timestamps not continuous after jump: %v", h.T)
	}
	// A backwards jump is handled the same way.
	h.add(&Status{TS: 5_000}, iv)
	if h.T[3]-h.T[2] != 2000 {
		t.Fatalf("backwards jump: %v", h.T)
	}
}
