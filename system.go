package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Temperature is one chip sensor.
type Temperature struct {
	Name string  `json:"name"`
	C    float64 `json:"c"`
}

// readTemps reads the thermal zones that carry a real value. On the 8800L the SoC sensors (tsens_tz_sensor0..4)
// report tenths of a degree and xo_therm whole degrees; the ambient, case and PA thermistors are not fitted
// (they read -40 or 125) and are skipped.
func readTemps(base string) []Temperature {
	dirs, _ := filepath.Glob(filepath.Join(base, "thermal_zone*"))
	sort.Slice(dirs, func(i, j int) bool { return zoneNum(dirs[i]) < zoneNum(dirs[j]) })
	var out []Temperature
	for _, d := range dirs {
		typ := readTrim(filepath.Join(d, "type"))
		v, err := strconv.ParseFloat(readTrim(filepath.Join(d, "temp")), 64)
		if err != nil || typ == "" {
			continue
		}
		var name string
		var c float64
		switch {
		case strings.HasPrefix(typ, "tsens_tz_sensor"):
			name, c = "Chip sensor "+strings.TrimPrefix(typ, "tsens_tz_sensor"), v/10
		case strings.HasPrefix(typ, "xo_therm"):
			name, c = "Crystal (XO)", v
			if v > 200 {
				c = v / 10
			}
		default:
			continue
		}
		if c <= -30 || c >= 120 {
			continue
		}
		out = append(out, Temperature{Name: name, C: c})
	}
	return out
}

func zoneNum(p string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(filepath.Base(p), "thermal_zone"))
	return n
}

// socTemp is the hottest chip sensor, for the summary and the screen widget.
func socTemp(ts []Temperature) *float64 {
	var best *float64
	for _, t := range ts {
		if strings.HasPrefix(t.Name, "Chip") && (best == nil || t.C > *best) {
			best = fp(t.C)
		}
	}
	return best
}

// Flash is the wear of the main flash (UBI device 0, the root file system).
type Flash struct {
	MaxEraseCount int     `json:"max_erase_count"`
	BadBlocks     int     `json:"bad_blocks"`
	ReservedBad   int     `json:"reserved_bad"` // spare blocks left for future bad blocks
	TotalBlocks   int     `json:"total_blocks"`
	FreeMB        float64 `json:"free_mb"`
	SizeMB        float64 `json:"size_mb"`
}

func readFlash(base, mount string) *Flash {
	d := filepath.Join(base, "ubi0")
	n := func(f string) (int, bool) {
		v, err := strconv.Atoi(readTrim(filepath.Join(d, f)))
		return v, err == nil
	}
	ec, ok := n("max_ec")
	if !ok {
		return nil
	}
	f := &Flash{MaxEraseCount: ec}
	f.BadBlocks, _ = n("bad_peb_count")
	f.ReservedBad, _ = n("reserved_for_bad")
	f.TotalBlocks, _ = n("total_eraseblocks")
	var st syscall.Statfs_t
	if mount != "" && syscall.Statfs(mount, &st) == nil {
		f.FreeMB = float64(st.Bavail) * float64(st.Bsize) / 1e6
		f.SizeMB = float64(st.Blocks) * float64(st.Bsize) / 1e6
	}
	return f
}

// Firewall is what the stock iptables rules did to connections coming from the internet (LTE side).
type Firewall struct {
	Policy        string `json:"policy"`         // INPUT policy: DROP once LTE is up
	PolicyDropped int64  `json:"policy_dropped"` // packets dropped by the policy, any interface but Wi-Fi and loopback
	WANRejected   int64  `json:"wan_rejected"`   // new connections to the router's internet address refused
	WANClosed     bool   `json:"wan_closed"`     // the LTE side is closed for new connections
}

var policyLine = regexp.MustCompile(`^Chain INPUT \(policy (\w+) (\d+) packets`)

// parseIptablesInput reads `iptables -L INPUT -v -n -x`.
func parseIptablesInput(out, wan string) Firewall {
	var f Firewall
	for _, l := range strings.Split(out, "\n") {
		if m := policyLine.FindStringSubmatch(l); m != nil {
			f.Policy = m[1]
			f.PolicyDropped, _ = strconv.ParseInt(m[2], 10, 64)
			continue
		}
		x := strings.Fields(l)
		if len(x) < 6 || x[2] != "REJECT" && x[2] != "DROP" {
			continue
		}
		if wan != "" && x[5] != wan {
			continue
		}
		n, err := strconv.ParseInt(x[0], 10, 64)
		if err != nil {
			continue
		}
		f.WANRejected += n
		if strings.Contains(l, "state NEW") || x[2] == "DROP" {
			f.WANClosed = true
		}
	}
	if f.Policy == "DROP" {
		f.WANClosed = true
	}
	return f
}

// Versions are the router's firmware and hardware identifiers that are safe to show.
type Versions struct {
	Model    string `json:"model"`
	Maker    string `json:"maker"`
	MiFiOS   string `json:"mifios"`
	Modem    string `json:"modem_fw"`
	PRI      string `json:"pri"`
	Hardware string `json:"hardware"`
	Kernel   string `json:"kernel"`
	WiFiChip string `json:"wifi_chip"`
	WiFiDrv  string `json:"wifi_driver"`
}

// Identity is kept on the router and sent to the browser only when the user presses Show.
type Identity struct {
	IMEI  string `json:"imei"`
	IMSI  string `json:"imsi"`
	ICCID string `json:"iccid"`
}

var fwLine = regexp.MustCompile(`(?m)^FW Version:\[(\S+)[^\[\n]*(?:\[(\d{4}-\d\d-\d\d))?`)

// parseGetInfo reads `modem2_cli get_info`.
func parseGetInfo(out string) (Versions, Identity, bool) {
	kv := parseKV(out)
	var v Versions
	var id Identity
	// "FW Version:[SDx20ALP-1.22.11  1  [2020-04-13 08:37:23]]" has brackets inside, so it is read on its own.
	m := fwLine.FindStringSubmatch(out)
	if m == nil {
		return v, id, false
	}
	v.Modem = m[1]
	if m[2] != "" {
		v.Modem += " (" + m[2] + ")"
	}
	v.Hardware = kv["hw version"].v
	v.Model = kv["model"].v
	v.PRI = kv["pri version"].v
	v.Maker = kv["manufacture"].v
	id.IMEI, id.IMSI, id.ICCID = kv["imei"].v, kv["imsi"].v, kv["iccid"].v
	return v, id, true
}

// parseWLVer reads `wl ver`: "wl0: Jul 24 2019 11:41:39 version 9.86.13 (r831412) FWID ...".
func parseWLVer(out string) string {
	if i := strings.Index(out, "version "); i >= 0 {
		if f := strings.Fields(out[i+8:]); len(f) > 0 {
			return f[0]
		}
	}
	return ""
}

// parseRevinfo turns `wl revinfo` chipnum 0xaa4c into "BCM43596".
func parseRevinfo(out string) string {
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && f[0] == "chipnum" {
			if n, err := strconv.ParseUint(strings.TrimPrefix(f[1], "0x"), 16, 32); err == nil {
				return "BCM" + strconv.FormatUint(n, 10)
			}
		}
	}
	return ""
}

func kernelVersion() string {
	f := strings.Fields(readTrim("/proc/version"))
	if len(f) >= 3 {
		return f[2]
	}
	return ""
}

func readMiFiOS() string { return readTrim("/opt/nvtl/etc/mifios-version") }

// SIM is the card state and how many PIN / PUK tries are left.
type SIM struct {
	State       string `json:"state"` // ready, pin, puk, absent, error, unknown
	PINRetries  *int   `json:"pin_retries"`
	PUKRetries  *int   `json:"puk_retries"`
	StateDetail string `json:"detail"`
}

// parseSIMStatus reads `modem2_cli sim_get_status`: "status is:[3] (SIM_READY)", "retries is:[3]", "unblocks is:[10]".
func parseSIMStatus(out string) (SIM, bool) {
	var s SIM
	if err := checkRC("modem2_cli", out); err != nil {
		return s, false
	}
	kv := parseKV(out)
	st, ok := kv["status is"]
	if !ok {
		return s, false
	}
	s.StateDetail = st.note
	n := strings.ToUpper(st.note)
	switch {
	case strings.Contains(n, "READY"):
		s.State = "ready"
	case strings.Contains(n, "PUK"):
		s.State = "puk"
	case strings.Contains(n, "PIN"):
		s.State = "pin"
	case strings.Contains(n, "ABSENT") || strings.Contains(n, "NOT_INSERTED") || strings.Contains(n, "NO_SIM"):
		s.State = "absent"
	case strings.Contains(n, "ERR"):
		s.State = "error"
	default:
		s.State = "unknown"
	}
	if v, err := strconv.Atoi(kv["retries is"].v); err == nil {
		s.PINRetries = &v
	}
	if v, err := strconv.Atoi(kv["unblocks is"].v); err == nil {
		s.PUKRetries = &v
	}
	return s, true
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
