package main

import "testing"

// Real output from the 8800L on Vodafone UA 3G (2026-11-02).
const modem2State3G = `cmd_get_state returned 0 (MDM_MAIN: success.)
state:[Connected]
reg state:[1]

Current Signal

tech:[9] (HSPA+)
roam:[0]
rssi:[-60]
dbm:[0]
bars:[4]
ecio:[-3]
sinr:[0]
rsrq:[0]
snr:[0]
rsrp:[0]
tx power:[0]
radio temp[0]
oper name:[Vodafone UA]
oper id:[25501]
cell id:[86690833]
lac:[3221]
femto:[0]
p_rev:[0]
sid:[0]
nid:[0]
`

// Real output, answered with "1" (WWAN) on stdin.
const ccm2WANStats = `Wan:[1=WWAN]
Wan:[2=WiFi WAN]
Wan:[3=Ethernet WAN]
What is WAN:  ccm_get_curr_wan_stats returned 0 (CCM: success.)
Wan:[WWAN]
V4 Connection State:[Connect]
V4 Bytes Rx:[246454]
V4 Bytes Tx:[1468411]
V4 Duration:[2335]
V4 ifc:[rmnet_data0]
V4 IP Addr:[10.62.20.52]
V4 Subnet:[255.255.255.248]
V4 Gateway:[10.62.20.53]
V6 Connection State:[Waiting]
V6 Bytes Rx:[0]
V6 Bytes Tx:[0]
V6 Duration:[0]
V6 ifc:[]
V6 IP Addr:[::]
V6 Gateway:[::]
`

// Real layout; MAC addresses and names replaced.
const dmdbList = `command [get_connected_device_list]

size 573604 bytes passed in.
cmd_get_connected_device_list returned 0 (DMDB: success.)
count:[2]
list[0].mac:[F2:00:00:00:00:01]
list[0].settings.name:[]
list[0].entry.hostname:[phone]
list[0].entry.interface:[WIFI Primary]
list[0].data.connected:[1]
list[0].data.ipv4:[192.168.1.2]
list[0].data.wifi_band:[2]

list[1].mac:[54:00:00:00:00:02]
list[1].settings.name:[Kitchen laptop]
list[1].entry.hostname:[user-laptop]
list[1].entry.param_req_list:[\001\002\006\014\017\032\034y\003!()*w\371\374\021]
list[1].data.connected:[1]
list[1].data.ipv4:[192.168.1.4]
`

func TestParseModem2State3G(t *testing.T) {
	n, s, err := parseModem2State(modem2State3G)
	if err != nil {
		t.Fatal(err)
	}
	if n.Tech != "3G" || n.TechName != "HSPA+" || n.Operator != "Vodafone UA" || n.Reg != "home" ||
		n.CellID != "86690833" || n.TAC != "3221" {
		t.Errorf("network %+v", n)
	}
	if s.RSSI == nil || *s.RSSI != -60 || s.EcIo == nil || *s.EcIo != -3 || s.RSCP == nil || *s.RSCP != -63 {
		t.Errorf("3G signal %v %v %v", s.RSSI, s.EcIo, s.RSCP)
	}
	if s.RSRP != nil || s.RSRQ != nil || s.SINR != nil {
		t.Errorf("LTE values must be empty on 3G: %v %v %v", s.RSRP, s.RSRQ, s.SINR)
	}
}

// Real output on LTE B3 after enabling B3 (2026-11-02). sinr stays 0; snr carries the SINR.
const modem2StateLTE = `cmd_get_state returned 0 (MDM_MAIN: success.)
state:[Connected]
reg state:[1]

Current Signal

tech:[10] (LTE)
roam:[0]
rssi:[-41]
dbm:[0]
bars:[5]
ecio:[0]
sinr:[0]
rsrq:[-8]
snr:[17]
rsrp:[-69]
tx power:[0]
radio temp[-1301287780]
oper name:[Vodafone UA]
oper id:[25501]
cell id:[143415071]
lac:[0]
femto:[0]
p_rev:[0]
sid:[0]
nid:[0]
`

func TestParseModem2StateLTE(t *testing.T) {
	n, s, err := parseModem2State(modem2StateLTE)
	if err != nil {
		t.Fatal(err)
	}
	if n.Tech != "LTE" || n.Reg != "home" || n.CellID != "143415071" {
		t.Errorf("network %+v", n)
	}
	if s.RSRP == nil || *s.RSRP != -69 || s.RSRQ == nil || *s.RSRQ != -8 || s.EcIo != nil {
		t.Errorf("LTE signal %v %v %v", s.RSRP, s.RSRQ, s.EcIo)
	}
	if s.SINR == nil || *s.SINR != 17 {
		t.Errorf("SINR must come from snr (17), got %v", s.SINR)
	}
}

func TestParseModem2StateErrors(t *testing.T) {
	if _, _, err := parseModem2State("cmd_get_state returned 1 (MDM_MAIN: general error.)\n"); err == nil {
		t.Error("a failed call must be an error")
	}
	if _, _, err := parseModem2State("something else\n"); err == nil {
		t.Error("unexpected output must be an error")
	}
}

func TestParseWANStats(t *testing.T) {
	w, err := parseWANStats(ccm2WANStats)
	if err != nil {
		t.Fatal(err)
	}
	if w.state != "connected" || w.iface != "rmnet_data0" || w.ip != "10.62.20.52" || w.sessionS != 2335 {
		t.Errorf("got %+v", w)
	}
}

func TestParseDmdbClientsAndMerge(t *testing.T) {
	live, err := parseDmdbClients(dmdbList)
	if err != nil || len(live) != 2 {
		t.Fatalf("got %+v, %v", live, err)
	}
	if live[0].Name != "phone" || live[0].MAC != "f2:00:00:00:00:01" || !live[0].Online || live[1].Name != "Kitchen laptop" {
		t.Errorf("got %+v", live)
	}
	base := []Client{
		{IP: "192.168.1.9", MAC: "aa:00:00:00:00:09", Name: "tv"},
		{IP: "192.168.1.2", MAC: "f2:00:00:00:00:01"},
	}
	all := mergeClients(base, live)
	if len(all) != 3 || !all[0].Online || all[2].Name != "tv" || all[2].Online {
		t.Errorf("merged %+v", all)
	}
}

func TestTechClass(t *testing.T) {
	for name, want := range map[string]string{"HSPA+": "3G", "LTE": "LTE", "UMTS": "3G", "EDGE": "2G", "": "", "NONE": ""} {
		if got := techClass(name); got != want {
			t.Errorf("techClass(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestVendorAllowList(t *testing.T) {
	v := &Vendor{dir: "/nonexistent"}
	for _, sub := range []string{"lte_band_set_enabled", "efs_write", "factory_reset", "update_signal_lte"} {
		if _, err := v.run("modem2_cli", sub, "", 1); err == nil {
			t.Errorf("%s must be refused", sub)
		}
	}
}
