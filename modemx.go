package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Slower-changing modem and router facts read with the stock tools: data session details, network time
// zone, reject cause, band preference, LTE search timer, unread SMS count, lifetime traffic.

// WANDetails are the data session's addresses from the network.
type WANDetails struct {
	APN     string   `json:"apn"`
	Gateway string   `json:"gateway"`
	Mask    string   `json:"mask"`
	DNS     []string `json:"dns"`
}

// parseCallStatus reads `modem2_cli call_get_status` (stdin "1\n0\n" = Internet profile, IPv4).
func parseCallStatus(out string) (WANDetails, bool) {
	var d WANDetails
	if err := checkRC("modem2_cli", out); err != nil {
		return d, false
	}
	kv := parseKV(out)
	if _, ok := kv["wwan state"]; !ok {
		return d, false
	}
	clean := func(s string) string {
		if s == "0.0.0.0" || s == "::" {
			return ""
		}
		return s
	}
	d.Gateway = clean(kv["gateway"].v)
	d.Mask = clean(kv["mask"].v)
	for _, k := range []string{"dns 1", "dns 2"} {
		if v := clean(kv[k].v); v != "" {
			d.DNS = append(d.DNS, v)
		}
	}
	return d, true
}

// parseCGCONTRDP reads "+CGCONTRDP: cid,bearer,apn,local addr[ and mask],gw,dns1,dns2" for the first context.
func parseCGCONTRDP(lines []string) (apn string, dns []string) {
	v, ok := findLine(lines, "+CGCONTRDP:")
	if !ok {
		return "", nil
	}
	f := splitFields(v)
	if len(f) >= 3 {
		apn = f[2]
	}
	for i := 5; i < len(f) && i < 7; i++ {
		if f[i] != "" && f[i] != "0.0.0.0" {
			dns = append(dns, f[i])
		}
	}
	return apn, dns
}

// parseNetworkTime reads `modem2_cli get_network_time`: "Time zone:[8]" in quarter hours (8 = UTC+2).
func parseNetworkTime(out string) (tzMin *int, dst int, ok bool) {
	if err := checkRC("modem2_cli", out); err != nil {
		return nil, 0, false
	}
	kv := parseKV(out)
	q, err := strconv.Atoi(kv["time zone"].v)
	if err != nil || q < -48 || q > 56 {
		return nil, 0, false
	}
	dst, _ = strconv.Atoi(kv["dst"].v)
	return ip(q * 15), dst, true
}

// tzText formats minutes east of UTC as "UTC+2" or "UTC+5:30".
func tzText(min int) string {
	sign := "+"
	if min < 0 {
		sign, min = "−", -min
	}
	if min%60 == 0 {
		return fmt.Sprintf("UTC%s%d", sign, min/60)
	}
	return fmt.Sprintf("UTC%s%d:%02d", sign, min/60, min%60)
}

// parseRejectCause reads `modem2_cli get_reject_cause_code`: "Reject cause code:[15]".
func parseRejectCause(out string) (int, bool) {
	if err := checkRC("modem2_cli", out); err != nil {
		return 0, false
	}
	v, err := strconv.Atoi(parseKV(out)["reject cause code"].v)
	return v, err == nil
}

// rejectText explains the network's (EMM/MM) reject cause, 3GPP TS 24.301 / 24.008.
func rejectText(c int) string {
	m := map[int]string{
		2: "SIM unknown to the network", 3: "SIM not allowed (illegal UE)", 5: "device (IMEI) not accepted",
		6: "device not allowed (illegal ME)", 7: "LTE service not allowed for this SIM",
		8: "LTE and 2G/3G services not allowed", 9: "the network could not identify the SIM",
		10: "detached by the network", 11: "operator network not allowed", 12: "this area is not allowed",
		13: "roaming not allowed in this area", 14: "LTE not allowed on this operator",
		15: "no suitable cells in this area", 16: "voice core temporarily unreachable", 17: "network failure",
		18: "voice (CS) domain not available", 19: "data connection setup failed (ESM failure)",
		20: "authentication failed (MAC failure)", 21: "authentication out of sync", 22: "network congestion",
		25: "not allowed in this closed group (CSG)", 35: "requested service not allowed on this operator",
		39: "voice service temporarily unavailable", 40: "no data bearer active", 42: "severe network failure",
		95: "invalid message", 96: "missing information", 97: "unknown message", 98: "message not expected now",
		99: "unknown information element", 100: "conditional information element error", 111: "protocol error",
	}
	if t, ok := m[c]; ok {
		return t
	}
	return "code " + strconv.Itoa(c)
}

// parseCEER reads "+CEER: <text>" (the modem's last call / connection failure report).
func parseCEER(lines []string) string {
	v, ok := findLine(lines, "+CEER:")
	if !ok {
		return ""
	}
	v = strings.Trim(strings.TrimSpace(v), `"`)
	if strings.EqualFold(v, "No cause information available") || v == "" {
		return ""
	}
	return v
}

// parseBSR reads `modem2_cli bsr_get_timers`: "Next lte scan:[300]" (seconds; how often a 3G modem looks for LTE).
func parseBSR(out string) (nextLTE int, ok bool) {
	if err := checkRC("modem2_cli", out); err != nil {
		return 0, false
	}
	v, err := strconv.Atoi(parseKV(out)["next lte scan"].v)
	return v, err == nil && v > 0
}

// parseBandEnabled reads `modem2_cli lte_band_get_enabled` (band number on stdin): "LTE band enabled:[1]".
func parseBandEnabled(out string) (bool, bool) {
	if err := checkRC("modem2_cli", out); err != nil {
		return false, false
	}
	switch parseKV(out)["lte band enabled"].v {
	case "1":
		return true, true
	case "0":
		return false, true
	}
	return false, false
}

var diagBand = regexp.MustCompile(`E_UTRA_BAND_(\d+)`)

// parseDiagBands takes the "LTE Band Class:" list from `modem2_cli get_diag_info`: the bands in effect since
// the modem last started (a changed preference shows up here only after a restart).
func parseDiagBands(out string) ([]int, bool) {
	for _, l := range strings.Split(out, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "LTE Band Class:") {
			continue
		}
		var bands []int
		for _, m := range diagBand.FindAllStringSubmatch(l, -1) {
			bands = append(bands, atoi(m[1]))
		}
		return bands, true
	}
	return nil, false
}

// parseMsgBusInt reads a message bus value: "data type:[int32]" then "data:[20]".
func parseMsgBusInt(out string) (int, bool) {
	if err := checkRC("msgbus_cli", out); err != nil {
		return 0, false
	}
	v, err := strconv.Atoi(parseKV(out)["data"].v)
	return v, err == nil && v >= 0
}

// Lifetime is the traffic the stock software counted since the router was first used.
type Lifetime struct {
	Down int64 `json:"down"`
	Up   int64 `json:"up"`
}

// parseUsageStats reads `dmdb_cli get_data_usage_statistics` (stdin "\n\n\n-1\n" = all bearers):
// tx_* and rx_* per roaming type, from the router's point of view (rx = downloads).
func parseUsageStats(out string) (Lifetime, bool) {
	var l Lifetime
	if err := checkRC("dmdb_cli", out); err != nil {
		return l, false
	}
	found := false
	for k, v := range parseKV(out) {
		n, err := strconv.ParseInt(v.v, 10, 64)
		if err != nil || n < 0 {
			continue
		}
		switch {
		case strings.HasPrefix(k, "rx_"):
			l.Down += n
			found = true
		case strings.HasPrefix(k, "tx_"):
			l.Up += n
			found = true
		}
	}
	return l, found
}

// Bands of the 8800L's LTE radio, and the ones Ukrainian operators use.
var (
	hwLTEBands = []int{2, 3, 4, 5, 7, 13, 14, 20, 28, 46, 48, 66}
	uaLTEBands = map[int]bool{1: true, 3: true, 7: true, 8: true, 20: true}
)

// BandInfo is one LTE band in the Modem section of the Cells page.
type BandInfo struct {
	Band     int   `json:"band"`
	Hardware bool  `json:"hardware"` // the router's radio has it
	Pref     *bool `json:"pref"`     // in the band preference (nil = cannot be read: bands above 43)
	Applied  bool  `json:"applied"`  // in effect since the modem started
	UsedHere bool  `json:"used_here"`
}

// ModemConfig is the read-only Modem section.
type ModemConfig struct {
	Bands      []BandInfo `json:"bands"`
	PrefAllOn  bool       `json:"pref_all_on"` // every readable band (1-43) is on
	PrefOff    []int      `json:"pref_off"`    // readable bands switched off
	ReadAgoS   int64      `json:"read_ago_s"`
	NextLTEScS *int       `json:"next_lte_scan_s"` // on 3G: how often the modem looks for LTE
}

func buildBands(pref map[int]bool, applied []int) ModemConfig {
	var mc ModemConfig
	ap := map[int]bool{}
	for _, b := range applied {
		ap[b] = true
	}
	hw := map[int]bool{}
	for _, b := range hwLTEBands {
		hw[b] = true
	}
	list := map[int]bool{}
	for _, b := range hwLTEBands {
		list[b] = true
	}
	for b := range uaLTEBands {
		list[b] = true
	}
	mc.PrefAllOn = len(pref) > 0
	for b := 1; b <= 43; b++ {
		if v, ok := pref[b]; ok && !v {
			mc.PrefOff = append(mc.PrefOff, b)
			mc.PrefAllOn = false
		}
	}
	for b := 1; b <= 71; b++ {
		if !list[b] {
			continue
		}
		bi := BandInfo{Band: b, Hardware: hw[b], Applied: ap[b], UsedHere: uaLTEBands[b]}
		if v, ok := pref[b]; ok {
			bi.Pref = bp(v)
		}
		mc.Bands = append(mc.Bands, bi)
	}
	return mc
}
