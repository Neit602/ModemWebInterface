package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixture returns one "### <header>" section of a masked raw output file in testdata/raw.
func fixture(t *testing.T, file, header string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata/raw", file))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	in := false
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "### ") {
			if in {
				break
			}
			in = strings.TrimSpace(l[4:]) == header
			continue
		}
		if in && !strings.HasPrefix(l, "@") {
			out = append(out, l)
		}
	}
	if !in && len(out) == 0 {
		t.Fatalf("%s: no section %q", file, header)
	}
	return strings.Join(out, "\n")
}

// sysfsFixture rebuilds the "### dump <dir>" sections of linux-wifi.txt (lines "path=value") as files under a temp dir.
func sysfsFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	b, err := os.ReadFile("testdata/raw/linux-wifi.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(l, "/sys/") {
			continue
		}
		p, v, ok := strings.Cut(l, "=")
		if !ok || strings.Contains(v, "read error") {
			continue
		}
		f := filepath.Join(root, p)
		_ = os.MkdirAll(filepath.Dir(f), 0o755)
		if err := os.WriteFile(f, []byte(strings.TrimSpace(v)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestPowerFromFixture(t *testing.T) {
	root := sysfsFixture(t)
	base := filepath.Join(root, "sys/class/power_supply")
	b := readBatteryFrom(base)
	r := readPowerDetails(base)
	var d batteryDrain
	fillPower(&b, r, &d, time.Now())
	if b.VoltageV == nil || *b.VoltageV != 4.277 || b.CurrentMA == nil || *b.CurrentMA != 84 || b.PowerW == nil || *b.PowerW != 0.36 {
		t.Errorf("battery %+v", b)
	}
	if b.Phase != "full" || b.TimeLeftS != nil || b.TempC == nil || *b.TempC != 28.4 {
		t.Errorf("phase %q left %v temp %v", b.Phase, b.TimeLeftS, b.TempC)
	}

	// On battery the current is shown as a drain whatever sign the gauge uses, and time left is estimated.
	on := Battery{Percent: ip(50), Status: "discharging", ExtPower: bp(false)}
	fillPower(&on, powerReading{voltageV: fp(3.8), currentMA: fp(550)}, &d, time.Now())
	if *on.CurrentMA != -550 || *on.PowerW != -2.09 || on.Phase != "on battery" || on.TimeLeftS == nil || !on.TimeLeftEst {
		t.Fatalf("on battery %+v", on)
	}
	if want := int64(4400 * 0.5 / 550 * 3600); *on.TimeLeftS != want {
		t.Errorf("time left %d, want %d", *on.TimeLeftS, want)
	}
	gauge := Battery{Percent: ip(50), Status: "discharging", ExtPower: bp(false)}
	fillPower(&gauge, powerReading{voltageV: fp(3.8), currentMA: fp(-550), tteS: 9000}, &batteryDrain{}, time.Now())
	if *gauge.CurrentMA != -550 || *gauge.TimeLeftS != 9000 || gauge.TimeLeftEst {
		t.Errorf("gauge time %+v", gauge)
	}
	ch := Battery{Status: "charging", ExtPower: bp(true)}
	fillPower(&ch, powerReading{currentMA: fp(-800), chargeType: "Fast"}, &batteryDrain{}, time.Now())
	if *ch.CurrentMA != 800 || ch.Phase != "fast charging" {
		t.Errorf("charging %+v", ch)
	}
}

func TestDsmState(t *testing.T) {
	m := parseDsmState(fixture(t, "vendor-getters.txt", "dsm_cli get_state"))
	if m["power_on_cause"] != "power_button" || m["battery_temperature"] != "278" || m["power_state"] != "online_usb" {
		t.Errorf("%v", m)
	}
	if powerOnCauseText("power_button") != "Power button" || powerOnCauseText("some_new_cause") != "Some new cause" {
		t.Error("power on cause text")
	}
}

func TestThermalAndFlash(t *testing.T) {
	root := sysfsFixture(t)
	ts := readTemps(filepath.Join(root, "sys/class/thermal"))
	if len(ts) != 6 {
		t.Fatalf("temps %+v", ts)
	}
	if ts[0].Name != "Crystal (XO)" || ts[0].C != 33 || ts[1].Name != "Chip sensor 0" || ts[1].C != 37 {
		t.Errorf("temps %+v", ts)
	}
	if v := socTemp(ts); v == nil || *v != 37.7 {
		t.Errorf("soc %v", v)
	}
	f := readFlash(filepath.Join(root, "sys/class/ubi"), "")
	if f == nil || f.MaxEraseCount != 144 || f.BadBlocks != 2 || f.ReservedBad != 38 || f.TotalBlocks != 1484 {
		t.Errorf("flash %+v", f)
	}
}

func TestIptablesInput(t *testing.T) {
	f := parseIptablesInput(fixture(t, "linux-wifi.txt", "iptables -L -v -n -x"), "rmnet_data0")
	if f.Policy != "DROP" || f.PolicyDropped != 3 || !f.WANClosed || f.WANRejected != 0 {
		t.Errorf("%+v", f)
	}
	open := parseIptablesInput("Chain INPUT (policy ACCEPT 120 packets, 9000 bytes)\n    pkts      bytes target     prot opt in     out     source               destination\n", "rmnet_data0")
	if open.WANClosed || open.Policy != "ACCEPT" {
		t.Errorf("open firewall %+v", open)
	}
	rej := parseIptablesInput("Chain INPUT (policy DROP 0 packets, 0 bytes)\n      17     1000 REJECT     tcp  --  rmnet_data0 *       0.0.0.0/0            10.1.1.1            state NEW reject-with tcp-reset\n       5      300 REJECT     tcp  --  rndis0 *       0.0.0.0/0            10.1.1.1            state NEW\n", "rmnet_data0")
	if rej.WANRejected != 17 {
		t.Errorf("rejected %+v", rej)
	}
}

func TestWiFiParsers(t *testing.T) {
	cs, ok := parseChanspec(fixture(t, "linux-wifi.txt", "/opt/nvtl/bin/wl -i wlan0 chanspec"))
	if !ok || cs != (Chanspec{Band: "5 GHz", Primary: 161, Center: 155, Width: 80}) || cs.FreqMHz() != 5805 {
		t.Errorf("wlan0 %+v", cs)
	}
	cs, ok = parseChanspec(fixture(t, "linux-wifi.txt", "/opt/nvtl/bin/wl -i wlan1 chanspec"))
	if !ok || cs != (Chanspec{Band: "2.4 GHz", Primary: 6, Center: 6, Width: 20}) || cs.FreqMHz() != 2437 {
		t.Errorf("wlan1 %+v", cs)
	}
	ssid, noise := parseWLStatus(fixture(t, "linux-wifi.txt", "/opt/nvtl/bin/wl -i wlan0 status"))
	if ssid != "Verizon-MiFi8800L-0000" || noise == nil || *noise != -89 {
		t.Errorf("status %q %v", ssid, noise)
	}
	busy, others := parseChanim(fixture(t, "linux-wifi.txt", "/opt/nvtl/bin/wl -i wlan1 chanim_stats"))
	if busy == nil || *busy != 9 || others == nil || *others != 2 {
		t.Errorf("chanim %v %v", busy, others)
	}
	if m := parseAssoclist(fixture(t, "linux-wifi.txt", "/opt/nvtl/bin/wl -i wlan0 assoclist")); len(m) != 1 || m[0] != "02:00:00:00:00:01" {
		t.Errorf("assoclist %v", m)
	}
	s, ok := parseStaInfo(fixture(t, "linux-wifi.txt", "/opt/nvtl/bin/wl -i wlan0 sta_info 02:00:00:00:00:01"))
	if !ok || s.rssi == nil || *s.rssi != -66 || *s.txRate != 6 || *s.rxRate != 6 || s.connectedS != 305 || s.idleS != 5 ||
		s.txBytes != 8797 || s.rxBytes != 23151 {
		t.Errorf("sta_info %+v", s)
	}
	if v := parseWLVer(fixture(t, "linux-wifi.txt", "/opt/nvtl/bin/wl -i wlan0 ver")); v != "9.86.13" {
		t.Errorf("ver %q", v)
	}
	if v := parseRevinfo(fixture(t, "linux-wifi.txt", "/opt/nvtl/bin/wl -i wlan0 revinfo")); v != "BCM43596" {
		t.Errorf("chip %q", v)
	}
}

func TestScanAndOverlap(t *testing.T) {
	var list []Neighbour
	list = append(list, parseScanResults(fixture(t, "wifi-scan.txt", "wlan0 (5 GHz) scan"))...)
	list = append(list, parseScanResults(fixture(t, "wifi-scan.txt", "wlan1 (2.4 GHz) scan"))...)
	if len(list) != 3 {
		t.Fatalf("neighbours %+v", list)
	}
	if list[2].SSID != "ssid-3" || list[2].RSSI != -33 || list[2].Chanspec != (Chanspec{Band: "2.4 GHz", Primary: 11, Center: 9, Width: 40}) || !list[2].Secure {
		t.Errorf("ssid-3 %+v", list[2])
	}
	radios := []WiFiRadio{
		{Iface: "wlan0", Network: "Primary", SSID: "Verizon-MiFi8800L-0000", Up: true, Chanspec: Chanspec{Band: "5 GHz", Primary: 161, Center: 155, Width: 80}},
		{Iface: "wlan1", Network: "Guest", SSID: "Verizon-MiFi8800L-Guest-0000", Up: true, Chanspec: Chanspec{Band: "2.4 GHz", Primary: 6, Center: 6, Width: 20}},
	}
	out, advice := markOverlaps(list, radios)
	got := map[string]string{}
	for _, n := range out {
		got[n.SSID] = n.Overlap
	}
	// ssid-1: 149/80 on the same 80 MHz block as ours; ssid-2 on ch 3 and ssid-3 on 9/40 overlap ch 6 partly.
	if got["ssid-1"] != "same" || got["ssid-2"] != "partly" || got["ssid-3"] != "partly" {
		t.Errorf("overlaps %v", got)
	}
	if out[0].Band != "5 GHz" || len(advice) != 2 {
		t.Errorf("order %+v advice %v", out, advice)
	}
	if !strings.Contains(advice[0], "Channel 36 (80 MHz) is free") {
		t.Errorf("5 GHz advice %q", advice[0])
	}
	// On 2.4 GHz ch 1 overlaps only the weak ch 3 network; ch 11 sits under the -33 dBm one.
	if !strings.Contains(advice[1], "channel 1 (20 MHz) is quieter") && !strings.Contains(advice[1], "Channel 1 (20 MHz)") {
		t.Errorf("2.4 GHz advice %q", advice[1])
	}
}

func TestModemParsers(t *testing.T) {
	v, id, ok := parseGetInfo(fixture(t, "vendor-getters.txt", "modem2_cli get_info"))
	if !ok || v.Modem != "SDx20ALP-1.22.11 (2020-04-13)" || v.Model != "MIFI8800L" || v.PRI != "151" || v.Hardware != "4" || v.Maker != "Inseego" {
		t.Errorf("versions %+v", v)
	}
	if id.IMEI != "990016870000000" || id.IMSI != "255010000000000" || id.ICCID != "8938000000000000000" {
		t.Errorf("identity %+v", id)
	}
	s, ok := parseSIMStatus(fixture(t, "vendor-getters.txt", "modem2_cli sim_get_status"))
	if !ok || s.State != "ready" || *s.PINRetries != 3 || *s.PUKRetries != 10 {
		t.Errorf("sim %+v", s)
	}
	d, ok := parseCallStatus(fixture(t, "vendor-getters-prompted.txt", `modem2_cli call_get_status | 1\n0\n`))
	if !ok || d.Gateway != "10.11.11.110" || d.Mask != "255.255.255.252" || len(d.DNS) != 2 {
		t.Errorf("call status %+v", d)
	}
	if _, ok := parseCallStatus(fixture(t, "vendor-getters.txt", "modem2_cli call_get_status")); ok {
		t.Error("call status without input should fail")
	}
	lines := strings.Split(fixture(t, "at-read-forms.txt", "AT+CGCONTRDP"), "\n")
	if apn, dns := parseCGCONTRDP(lines); apn != "internet" || len(dns) != 2 {
		t.Errorf("cgcontrdp %q %v", apn, dns)
	}
	tz, dst, ok := parseNetworkTime(fixture(t, "vendor-getters.txt", "modem2_cli get_network_time"))
	if !ok || *tz != 120 || dst != 0 || tzText(*tz) != "UTC+2" || tzText(330) != "UTC+5:30" || tzText(-180) != "UTC−3" {
		t.Errorf("tz %v %d", tz, dst)
	}
	if c, ok := parseRejectCause(fixture(t, "vendor-getters.txt", "modem2_cli get_reject_cause_code")); !ok || c != 0 {
		t.Errorf("reject %d", c)
	}
	if rejectText(15) != "no suitable cells in this area" || rejectText(250) != "code 250" {
		t.Error("reject text")
	}
	if n, ok := parseBSR(fixture(t, "vendor-getters.txt", "modem2_cli bsr_get_timers")); !ok || n != 300 {
		t.Errorf("bsr %d", n)
	}
	if on, ok := parseBandEnabled(fixture(t, "vendor-getters-prompted.txt", `modem2_cli lte_band_get_enabled | 3\n`)); !ok || !on {
		t.Error("band 3")
	}
	bands, ok := parseDiagBands(fixture(t, "vendor-getters-prompted.txt", "modem2_cli get_diag_info"))
	if !ok || len(bands) != 33 || bands[2] != 3 {
		t.Errorf("applied %v", bands)
	}
	if n, ok := parseMsgBusInt(fixture(t, "msgbus.txt", `msgbus_cli MsgBusGet | sms.list.unread\n`)); !ok || n != 20 {
		t.Errorf("sms %d", n)
	}
	l, ok := parseUsageStats(fixture(t, "vendor-getters-prompted.txt", `dmdb_cli get_data_usage_statistics | \n\n\n-1\n`))
	if !ok || l.Down != 399690510667 || l.Up != 24699257709 {
		t.Errorf("lifetime %+v", l)
	}
	if parseCEER(strings.Split(fixture(t, "at-read-forms.txt", "AT+CEER"), "\n")) != "" {
		t.Error("CEER without a cause should be empty")
	}
	if parseCEER([]string{"+CEER: Lost connection"}) != "Lost connection" {
		t.Error("CEER text")
	}

	pref := map[int]bool{1: true, 2: true, 3: false, 7: true, 20: true}
	mc := buildBands(pref, []int{1, 2, 7, 20})
	if mc.PrefAllOn || len(mc.PrefOff) != 1 || mc.PrefOff[0] != 3 {
		t.Errorf("pref %+v", mc)
	}
	for _, b := range mc.Bands {
		if b.Band == 3 && (b.Pref == nil || *b.Pref || b.Applied || !b.Hardware || !b.UsedHere) {
			t.Errorf("B3 %+v", b)
		}
		if b.Band == 66 && b.Pref != nil {
			t.Errorf("B66 cannot be read: %+v", b)
		}
	}
}

func TestDeviceUsage(t *testing.T) {
	u, err := readUsage(dmdbFixture)
	if err != nil {
		t.Fatal(err)
	}
	if u.session != 533 {
		t.Errorf("session %d", u.session)
	}
	d := u.byMAC["02:00:00:00:00:0b"]
	if d.SessDown != 112084 || d.SessUp != 13195 || d.TotalDown < d.SessDown {
		t.Errorf("usage %+v", d)
	}
}

func TestDmdbInterface(t *testing.T) {
	cl, err := parseDmdbClients(fixture(t, "vendor-getters.txt", "dmdb_cli get_connected_device_list"))
	if err != nil || len(cl) != 2 {
		t.Fatalf("%v %+v", err, cl)
	}
	if cl[0].Via != "usb" || cl[1].Via != "wifi" || cl[1].Network != "Primary" {
		t.Errorf("%+v", cl)
	}
}

// The panel must never run anything that prints Wi-Fi keys or the admin password, or changes settings.
func TestVendorAllowedHasNoSecretsOrSetters(t *testing.T) {
	for k := range vendorAllowed {
		sub := strings.Fields(k)[1]
		switch {
		case k == "ccm2_cli wwan_disconnect", k == "ccm2_cli wwan_connect": // the Restart data session button
		case strings.Contains(sub, "set"), strings.Contains(sub, "profile"), strings.Contains(sub, "login"),
			strings.Contains(sub, "password"), strings.Contains(sub, "key"), strings.Contains(sub, "efs"),
			strings.Contains(sub, "write"), strings.Contains(sub, "delete"), strings.Contains(sub, "reset"):
			t.Errorf("%s must not be allowed", k)
		}
	}
	for k := range wlAllowed {
		if strings.Contains(k, "wsec") || strings.Contains(k, "key") || strings.Contains(k, "pmk") {
			t.Errorf("wl %s must not be allowed", k)
		}
	}
}
