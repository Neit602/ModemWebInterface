package main

import (
	"os"
	"path/filepath"
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

func writeSysfs(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, v := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadBatteryTemp(t *testing.T) {
	// Layout of the 8800L (2026-11-05): the fuel gauge has temp_ambient, no temp; the charger
	// also says type=Battery but has no capacity.
	base := t.TempDir()
	writeSysfs(t, filepath.Join(base, "bq27520-battery"), map[string]string{
		"type": "Battery", "present": "1", "capacity": "100", "status": "Full",
		"temp_ambient": "284", "voltage_now": "4277000", "current_now": "84000"})
	writeSysfs(t, filepath.Join(base, "smb1351-charger"), map[string]string{
		"type": "Battery", "present": "1", "status": "Charging"})
	writeSysfs(t, filepath.Join(base, "usb"), map[string]string{"type": "USB", "online": "1"})
	b := readBatteryFrom(base)
	if b.Percent == nil || *b.Percent != 100 || b.Status != "full" {
		t.Fatalf("level/status: %+v", b)
	}
	if b.TempC == nil || *b.TempC != 28.4 {
		t.Fatalf("temp_ambient not used: %v", b.TempC)
	}
	if b.ExtPower == nil || !*b.ExtPower {
		t.Fatalf("ext power: %v", b.ExtPower)
	}

	// Other drivers: plain temp.
	base2 := t.TempDir()
	writeSysfs(t, filepath.Join(base2, "battery"), map[string]string{
		"type": "Battery", "capacity": "55", "status": "Discharging", "temp": "312"})
	if b := readBatteryFrom(base2); b.TempC == nil || *b.TempC != 31.2 {
		t.Fatalf("temp fallback: %v", b.TempC)
	}

	// Neither file: no temperature, no crash.
	base3 := t.TempDir()
	writeSysfs(t, filepath.Join(base3, "battery"), map[string]string{"type": "Battery", "capacity": "55"})
	if b := readBatteryFrom(base3); b.TempC != nil {
		t.Fatalf("expected no temperature, got %v", *b.TempC)
	}
}
