# Backstory and decisions

This file is the history of the project so far, written so a new session can pick up with full context.
It complements CLAUDE.md (device facts, rules) and README.md (install steps).

## About me

- I am in Ukraine, I write in Ukrainian. Replies to me in Ukrainian; code, comments, UI and docs in English.
- I care about practical results and low cost. My computer runs Linux (Arch); the router is on its USB.
- I have a ZTE MF920U and bought another budget modem as my daily driver; the 8800L is now a test bench.

## How this started

1. My MiFi 8800L (bought together with several identical units, which work fine) misbehaved on my main
   operator at home:
   - the connection dropped every few minutes and came back a couple of minutes later;
   - after attaching to the network there was no data until I ran a manual network search in the menu;
     data started flowing as the search finished;
   - it showed 3G all the time and gave 2.8 Mbps, while a ZTE MF920U in the next room with the same
     SIM, plan and operator gave 20+ Mbps.
   Swapping SIMs and operators did not change the picture in general.
2. Diagnosis: the 8800L is a Verizon (US) device. Of its LTE bands only B3 (1800), B7 (2600) and B20 (800)
   are used in Ukraine. Ukrainian operators also run LTE on B1 (2100, extra spectrum bought in 2024) and
   B8 (900, since 2020); the 8800L supports 2100 and 900 only as 3G (UMTS B1/B8). The MF920U has
   B1/B3/B5/B7/B8/B20/B28. So where my operator's best LTE cell at home is B1 or B8, the 8800L falls
   back to 3G. Later I confirmed that with another operator's SIM it does get 4G, so its LTE hardware works:
   it is not defective, it just lacks the bands that matter at my place.
3. "New firmware" was my first idea. Writing modem firmware is not possible: the Qualcomm modem image is
   signed. What is possible in theory is enabling LTE B1/B8 in the modem's RF NV settings, because the
   radio already handles those frequencies for 3G. From the XDA thread: one member claims B8 "should be
   enabled by default" on the 8800 and suggested a hard reset; bands are enabled/disabled in the rfnv;
   people enter diag mode with a tool called mifilink.exe and edit NV with QPST. Uncalibrated LTE on those
   bands may work poorly, and NV/EFS edits can brick the modem. This stays off limits (CLAUDE.md rule 1)
   unless I explicitly ask, and only after a full EFS/NV backup.
4. I bought another modem (budget about 2,000 UAH). Shortlist we discussed: Olax MT80 (bands
   B1/3/5/7/8/20/28/38/40/41, CRC9 antenna ports), TP-Link M7200 (B1/3/7/8/20, no antenna ports),
   ZTE MF79U (USB stick, TS9 ports). Lesson: avoid US-variant devices without B8.
5. Since the 8800L is now spare, the goal changed: use it as a small Linux computer and add my own
   features. First feature: a decent web UI, because the stock one is painful.

## What we built so far

- v0.1.0: mifi-panel in Ukrainian. v0.1.1: translated to English at my request.
  v0.2.0: adjusted to the real device after probe.txt (see "What probe.txt showed" in CLAUDE.md).
- Testing so far: demo mode checked in Chrome (desktop, phone width, dark mode, hover, keyboard);
  the AT code checked against a simulated modem on a pty (parsing, unsolicited lines, busy lock,
  actions, console validation); `-probe-at` checked against free and held ptys. Nothing besides
  probe.sh has run on the real router yet.

## Decisions and why

- Go, standard library only, one static ARMv7 binary (~6 MB): no package manager on the device, small RAM.
- UI is a single HTML file with inline JS/CSS and an embedded font, no CDN: the panel must work when the
  LTE link is down, which is exactly when it is needed.
- Font: Geologica variable (OFL), Latin + Cyrillic subsets, 41 KB total.
- Look: "instrument enamel" palette (grey-green surfaces, ink trace like a chart recorder), status colors
  always paired with an icon and a label, chart colors validated for color blindness.
- Signal quality thresholds (LTE): RSRP -80/-90/-100/-110 dBm, SINR 20/13/0 dB, RSRQ -10/-13/-16/-19 dB.
- Antenna aiming mode with a rising tone: I use external antennas on the TS9 ports.
- The Reconnect button automates my old workaround (manual search): AT+COPS=2 then AT+COPS=0.
- Default listening on loopback and br0 only: the stock firewall has no INPUT rules.
- Read-only probing first, every persistent change only with my OK.

## Right now (as of v0.2, kept for history; the current state is in "Resume here" at the end)

- The SIM in the router is the one that gets only 3G at home (my main operator). So expect:
  AT+COPS? reports access technology 2 (UTRAN), +CESQ gives RSCP and Ec/No while RSRP/RSRQ are 255
  (unknown), and probably no SINR.
- The UI is built around LTE metrics, so on 3G the signal block will say "No data". Planned fix once real
  output is seen: when the network is 3G, show RSCP (dBm) and Ec/Io (dB) as the main metrics, with 3G
  thresholds roughly RSCP -85/-95/-105 dBm and Ec/Io -6/-10/-14 dB.
- This SIM also reproduces my original problem (attached but no data until a manual search), which makes
  it a good test case. Useful read-only checks when that happens: AT+CGATT?, AT+CGACT?, AT+CGPADDR,
  AT+CEREG?/AT+CGREG?, and what the vendor tools report. Then see whether the panel's Reconnect fixes it.

## What the modem survey showed (2026-11-02, Vodafone UA SIM on 3G)

- AT ports: **/dev/smd8** and **/dev/smd7** answer (ATI: Novatel MIFI8800L, modem firmware 1.22.11 from
  2020-04-13). smd11 opens but stays silent. open() on smd21 and smd22 sleeps in the glink driver
  (no modem side behind them) and ignores O_NONBLOCK. On the very first open smd7 did not answer either,
  probably because the glink channel was still being set up and the command was lost.
- The first `-probe-at` hung forever: a goroutine sat in a blocking read on smd7 (glink_pkt_read) and
  Go's Close() waited for it. Since 0.3.0 modem ports are opened O_NONBLOCK, read only after ppoll(2)
  reports data, and open() runs in the background with a time limit (a pending open is never started
  twice). `-probe-at` now finishes in about 8 s and leaves nothing behind.
- Device holders: only port_bridge holds /dev/at_mdm0 and /dev/at_usb0. modem2d, modem_at_serverd,
  smsd, gpsd, dsmd hold /dev/diag but no smd port (they reach the modem over QMI), so smd8 is free.
- Standard 3GPP signal commands are useless here: AT+CESQ returns ERROR, AT+CSQ returns 99,99 even
  with good HSPA+ signal. AT+COPS? (AcT 2), +CREG/+CGREG (registered), +CGATT/+CGACT/+CGPADDR work.
- Qualcomm commands that work (AT+CLAC lists them): $QCSQ (on WCDMA: RSCP, Ec/Io, 0, 0, RSSI, and
  RSCP = RSSI + Ec/Io holds), $QCSYSMODE ("WCDMA - HSDPA and HSUPA"), ^SYSINFO (service, domain, roam,
  sysmode 5 = WCDMA, SIM), $CSQ, $QCRSRP? / $QCRSRQ? (LTE cells as PCI,EARFCN,"value": while on 3G it
  listed PCI 237 on EARFCN 2900 = B7 with odd values -12.70 / -1.30, to be checked on LTE),
  $QCRSCP? / $QCECNO? (empty on 3G), $QCBANDPREF? (lists WCDMA I/II/V/VIII only).
- modem2_cli (talks to modem2d, read commands are safe): get_signal / get_state give tech name, rssi, ecio,
  rsrp, rsrq, sinr, snr, bars, cell id, lac, operator; get_service_info, get_oper_info ("Freq band"),
  active_tech_get, active_band_get (0 on 3G), get_diag_info (Ec/Io, RSSI, error rate), sim_get_status.
  Write commands there (efs_write, lte_band_set_enabled, unlock_carrier, factory_reset...) are off limits.
- No UARFCN source found yet (not in modem2_cli or the AT list). EARFCN only via $QCRSRP?.
- ccm2_cli get_curr_wan_stats (answer "1" = WWAN on stdin) gives the data session state (Connect /
  Waiting / NotAvailable), interface, IP, session duration and byte counters. Its counters equal
  /proc/net/dev for rmnet_data0 exactly, so they do not fix a possible IPA offload undercount. With light
  traffic all counters agreed (wlan0 rx ≈ rmnet tx and back); the undercount check needs a real download
  (speed test on a phone) while comparing rmnet_data0, wlan0/wlan1 and /sys/kernel/debug/ipa. Not done yet.
- dmdb_cli get_connected_device_list lists the devices on Wi-Fi now: mac, settings.name (set in the
  stock UI), entry.hostname, data.ipv4, data.connected, entry.interface (WIFI Primary / Secondary).
- `modem2_cli get_signal` costs about 30 ms (25 ms CPU) per call.

## Band diagnostics (read-only, 2026-11-02, this unit, Vodafone 3G)

Raw output is in the conversation log; the format and what it says:
- `lte_band_get_enabled` asks "Enter LTE band:" on stdin and prints `LTE band enabled:[0|1]`.
  It accepts bands 1..43 only; 44 and above return rc 2 ("bad parameters") and print 0, so B46, B48 and
  B66 cannot be read this way (their 0 means "not asked", not "disabled").
  Result: every band 1..43 is 1 **except B3, which is 0**. Bands this hardware does not have at all
  (B1, B8, B12, B17, B38...) are 1 too, so the value is the modem's LTE band **preference** bit, not
  hardware support: a band is used only if it is in the preference and in the RF hardware.
  `get_diag_info` agrees: its "LTE Band Class" list is the same preference list and B3 is missing there.
  So on this unit LTE B3 (1800), the most common LTE band in Ukraine, looks switched off in the band
  preference. That would also explain part of the original 3G-only problem. To be confirmed by
  comparing with a working 8800L from the same batch (`echo 3 | modem2_cli lte_band_get_enabled`).
  Changing it is a band configuration change: rule 1, only on explicit request, after a backup.
- `lte_band_get_prior`: rc 1 (general error), no data.
- `active_band_get`: `Active band:[0]` on 3G; to read again on LTE.
- `band_class_get_enabled` lists BAND_CLASS_10 = 1 and BAND_CLASS_14 = 2 and asks which one; both
  answer `enabled:[0]`. These are CDMA band classes (Verizon legacy), irrelevant in Ukraine.
- `get_sup_tech`: `Available tech modes:[79] ( GSM UMTS CDMA EVDO)`. 79 is a bit mask (bits 0,1,2,3,6);
  the tool names only four of them. `enabled_tech_get` gives 18 = "UMTS LTE" (bits 1 and 4), so the two
  commands use different bit layouts, and the unnamed bit 6 is probably LTE. Do not read much into it.
- `check_lte_ca_status`: rc 1, "Failure" (not on LTE now).
- `ca_get_enabled`, `ca_bands_get_enabled`: rc 1 and a garbage number (uninitialised memory), i.e. no data.
  `ca_bands_get_enabled` asks for two band numbers on stdin; `ca_tri_bands_get_enabled` for three.
- `$QCBANDPREF?` (AT) lists only WCDMA I, II, V and VIII; it does not show the LTE preference.

## Version 0.3.0: wired to the real modem (2026-11-02)

- Signal and network: `modem2_cli get_state` every 4 s (`-radio-interval`). It is get_signal plus the
  registration state, at the same cost. 0 in its output means "not available" for that network type.
  On 3G: RSSI and Ec/Io from it, RSCP = RSSI + Ec/Io. On LTE: RSRP, RSRQ, SINR (SINR units unverified).
- AT on /dev/smd8 (`-at auto` picks it on the 8800L when no other program holds it): every third round
  only, read-only (+COPS?, +CEREG?/+CGREG?/+CREG?, $QCSQ on 3G, $QCRSRP? on LTE for the EARFCN and band).
  It takes over completely when modem2_cli fails. Nothing is sent when the port opens: the old
  ATE0 / AT+CMEE=2 / AT+CEREG=2 / AT+CGREG=2 / AT+COPS=3,0 at startup are gone.
- Data session: ccm2_cli get_curr_wan_stats (state, interface, IP, duration). Speed still from /proc/net/dev.
- Devices: dmdb_cli get_connected_device_list every third round, merged over the lease/ARP list.
- Vendor subcommands the panel can run are fixed in code (vendorAllowed in vendor.go).
- UI: on 3G the signal block switches to RSCP and Ec/Io (thresholds -85/-95/-105 dBm, -6/-10/-14 dB),
  the chart and antenna aiming follow; band shows "Not reported on 3G". Data session state and duration
  are shown. `?demo=3g` previews the 3G view.
- New action "Restart data session": ccm2_cli wwan_disconnect, then wwan_connect (both get an empty line
  for "optional call id"), then waits up to 60 s for an IP. Never run on the device yet: the first live test
  of it, and of Reconnect (AT+COPS=2/0), happens only together with me.
- Live check on the router (read-only, with -password, from /tmp): values matched the vendor tools
  (HSPA+, RSSI -58, Ec/Io -4, RSCP -62, cell 86690833, LAC 3221, data connected, 2 devices online),
  no warnings, no password = 401. CPU: router busy 0.9% without the panel, 3.4% with it; the panel itself
  about 0.55%, the vendor tools it starts about 1.8%. RSS 8.5 MB.
- Lesson about adb: `adb shell "cmd &"` did not start a detached process reliably and once left adb
  hanging; `adb shell "(setsid cmd </dev/null >log 2>&1 &)"` works.

## Still to verify on LTE (needs a SIM that gets LTE here)

- modem2 SINR units and whether 0 means "no value"; $QCRSRP? first entry = serving cell?
  (on 3G it listed PCI 237, EARFCN 2900 = B7, with odd values -12.70 / -1.30); active_band_get output.

## B3 experiment (2026-11-02, my explicit OK for this one change and its revert)

Plan: enable LTE B3 in the band preference with `modem2_cli lte_band_set_enabled`, watch for up to
5 minutes read-only, revert at once if service or the data session is lost for more than 3 minutes.

### Before (router uptime 1:08, clock not yet set, Vodafone UA SIM)

```
Thu Jan  1 01:08:48 UTC 1970
 01:08:48 up  1:08,  0 users,  load average: 1.13, 1.18, 1.19
### lte_band_get_enabled 1..43
B1=1 B2=1 B3=0 B4=1 B5=1 B6=1 B7=1 B8=1 B9=1 B10=1 B11=1 B12=1 B13=1 B14=1 B15=1 B16=1 B17=1 B18=1 B19=1 B20=1 B21=1 B22=1 B23=1 B24=1 B25=1 B26=1 B27=1 B28=1 B29=1 B30=1 B31=1 B32=1 B33=1 B34=1 B35=1 B36=1 B37=1 B38=1 B39=1 B40=1 B41=1 B42=1 B43=1 
### get_state
cmd_get_state returned 0 (MDM_MAIN: success.)
state:[Connected]
reg state:[1]

Current Signal

tech:[9] (HSPA+)
roam:[0]
rssi:[-61]
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
### get_diag_info
cmd_get_diag_info returned 0 (MDM_MAIN: success.)

[===============
Diagnostic Data
===============

NAM: N/A
Mobile Directory Number (MDN): N/A
IMSI_S: (removed)
Mobile Network Code (MNC): 01
Mobile Country Code (MCC): 255
Primary CDMA Channel A: 0
Secondary CDMA Channel A: 0
Primary CDMA Channel B: 0
Secondary CDMA Channel B: 0
Access Overload Class (ACCOLC): 0
Register On Home SID: N/A
Register On Foreign SID: N/A
Register On Foreign NID: N/A
CDMA Band Class: [WCDMA_EU_J_CH_IMT_2100] [WCDMA_US_PCS_1900] [WCDMA_US_850] [WCDMA_EU_J_900] 
LTE Band Class: [E_UTRA_BAND_1] [E_UTRA_BAND_2] [E_UTRA_BAND_4] [E_UTRA_BAND_5] [E_UTRA_BAND_6] [E_UTRA_BAND_7] [E_UTRA_BAND_8] [E_UTRA_BAND_9] [E_UTRA_BAND_10] [E_UTRA_BAND_11] [E_UTRA_BAND_12] [E_UTRA_BAND_13] [E_UTRA_BAND_14] [E_UTRA_BAND_17] [E_UTRA_BAND_18] [E_UTRA_BAND_19] [E_UTRA_BAND_20] [E_UTRA_BAND_21] [E_UTRA_BAND_24] [E_UTRA_BAND_25] [E_UTRA_BAND_28] [E_UTRA_BAND_33] [E_UTRA_BAND_34] [E_UTRA_BAND_35] [E_UTRA_BAND_36] [E_UTRA_BAND_37] [E_UTRA_BAND_38] [E_UTRA_BAND_39] [E_UTRA_BAND_40] [E_UTRA_BAND_41] [E_UTRA_BAND_42] [E_UTRA_BAND_43] 
Active Band: N/A
Slot Cycle Index: N/A
MIP Mode: MIP off
MIP Registration Retries: 0
MIP Registration Retries Timeout: 0
Reverse Tunneling: 0
DNS Primary: N/A
DNS Secondary: N/A
Supplemental Channel Request Message (SCRM): 0
Network ID (NID): 0
Error Rate: [UMTS]:2% 
RSSI: [0]:-67 
Ec/Io: [UMTS]:-4 
Latitude: 0
Longitude: 0
Tx Power (EVDO): 0
Rx Power 0 (EVDO): 0
Rx Power 1 (EVDO): 0
Packet Zone ID: 0
Call State: Unknown
Dormant State: Dormant
MAC Index: N/A
Subnet Mask: 255.255.255.248
Color Code: 0
UATI024: 00000000 00000000 00000000 00000000
PN Offset: N/A
]
### active_band_get
cmd_active_band_get returned 0 (MDM_MAIN: success.)
Active band:[0]
### AT$QCRSRP?
AT$QCRSRP?
$QCRSRP: 237,2900,"-012.80"
OK
### AT$QCRSRQ?
AT$QCRSRQ?
$QCRSRQ: 237,2900,"-01.70"
OK
### AT$QCSYSMODE?
AT$QCSYSMODE?
WCDMA - HSDPA and HSUPA
OK
### AT+COPS?
AT+COPS?
+COPS: 0,0,"Vodafone UA Vodafone UA",2
OK
### wan
What is WAN:  ccm_get_curr_wan_stats returned 0 (CCM: success.)
V4 Connection State:[Connect]
V4 Duration:[4103]
V4 ifc:[rmnet_data0]
V4 IP Addr:[10.62.20.52]
```

### Change (uptime 1:09)

Command, run on the router as root:

```
cd /opt/nvtl/bin && printf '3\n1\n' | timeout -t 15 ./modem2_cli lte_band_set_enabled
Enter LTE band: Enter enabled status: cmd_lte_band_set_enabled returned 0 (MDM_MAIN: success.)
```

The prompts are "Enter LTE band:" then "Enter enabled status:" (so the revert is `printf '3\n0\n'`).
Read back: B3 = 1, every other band 1..43 unchanged (all 1). The "LTE Band Class" list in
get_diag_info still has no B3, also 6 minutes later: it may show what the modem actually applied, or come
from another source. Unknown yet.

### Watch, 5 minutes, read-only every ~18 s (uptime 1:09 to 1:15)

- Always HSPA+ on the same cell (86690833, LAC 3221), reg state 1, RSSI -56..-66, Ec/Io -3..-9.
- Data session connected the whole time, same IP 10.62.20.52. No loss of service, no revert needed.
- active_band_get stayed 0. $QCRSRP? showed only PCI 237 on EARFCN 2900 (B7, values about -12.7),
  sometimes empty; no B3 cell (EARFCN 1200-1949) appeared.
- So the modem did not move to LTE by itself. Possible reasons: the preference is applied only on the
  next network selection or modem restart; or the modem stays on 3G while its data session is up
  (reselection to LTE normally happens when idle); or there is no usable B3 cell of Vodafone here.
  Next step, only with my OK: Reconnect (AT+COPS=2/0) or Restart data session.

### Reconnect test with B3 enabled (2026-11-02, router uptime ~1:25)

This computer gets its internet over another Wi-Fi (OLAX), not through the MiFi. The panel ran from /tmp
with a password; I pressed Reconnect on my phone. The watch read every 10 s over adb (modem2_cli
get_state, active_band_get, $QCRSRP? and $QCSYSMODE? on /dev/smd7 so it would not mix with the panel's
AT+COPS on /dev/smd8, ccm2_cli data session), with the same auto-revert rule. It was not needed.

- First live run of the panel's Reconnect: worked. "Detaching from the network…" then
  "Re-registration complete" about 10 s after the press. During it get_state showed state Idle, tech UMTS,
  data session Waiting with no IP; 10-20 s after the finish the data session was back with a new IP
  (10.62.20.52 -> 10.56.102.70) and HSPA+ on the same cell 86690833.
- The modem stayed on 3G for the 5 minutes after the reconnect. active_band_get stayed 0.
  $QCRSRP? now listed two LTE cells, both on EARFCN 2900 (B7): PCI 237 and PCI 165, values about -12.5
  to -13.2 (unit still unclear, not a plausible RSRP in dBm). **No B3 cell (EARFCN 1200-1949) appeared.**
- I also pressed Find all networks (17:22, first live run of it, about 40 s): "VODAFONE (LTE),
  Vodafone UA (3G, current), life:) (LTE, forbidden), UA-KYIVSTAR (LTE, forbidden)". So Vodafone's LTE
  network is visible here, but the modem keeps choosing 3G. After the scan it went back to automatic
  selection and stayed registered (+COPS: 0,0,"Vodafone UA",2).
- After the test: B3 still enabled (lte_band_get_enabled 3 -> 1); get_diag_info still lists no B3.
- Side note: the adb port forward on the computer disappeared once during the watch (adb server restart);
  a small loop now re-adds it. Watch scripts must not rely on adb forward alone.

Conclusion so far: enabling B3 in the preference and a fresh registration did not bring LTE. Either the
change takes effect only after a modem restart (get_diag_info not showing B3 hints at that), or there is no
usable Vodafone B3 here and the visible LTE is B7, which the modem does not pick for another reason.
Next proposed test, only with my OK: reboot the router (fresh modem start), then check whether B3 is
still enabled and whether the modem finds LTE.

### Reboot test with B3 enabled (2026-11-02 17:44, `adb shell reboot` with my OK)

- Before: B3 = 1, uptime 1:50, panel stopped. The router was back on adb and in runlevel 3 with modem2d
  running about 45 s after the reboot (17:45:09); the clock was set right away this time.
- After the reboot: **B3 still enabled** (lte_band_get_enabled 3 -> 1), so the setting is persistent, and
  **get_diag_info now lists E_UTRA_BAND_3**: the band preference takes effect only when the modem starts.
- **The modem came up on LTE B3 straight away** and stayed there for the whole 5-minute watch (31 samples,
  no drop): Vodafone UA, tech 10 (LTE), active_band_get 3, cell id 143415071, PCI 80 on EARFCN 1700,
  RSRP -68..-75 dBm, RSRQ -9..-14 dB (one -3), snr 12..24, data session connected (IP 10.58.8.156).
- **So the 3G-only problem on this unit (at least with Vodafone at home) was B3 being switched off in the
  LTE band preference**, not missing hardware. Enabling it took one NV-backed setting and a reboot.
- What the outputs mean on LTE (this settles the "to verify" items):
  - modem2 get_state: `sinr` stays 0 on LTE; the SINR-like value is in `snr` (dB, 12..24 here).
    The panel reads `sinr`, so it shows 0.0 dB on LTE: to fix (use snr).
  - modem2 `ecio` is 0 on LTE, `lac` is 0 (TAC is not reported there).
  - `$QCRSRP?` lists the serving cell first (PCI 80, EARFCN 1700, value = its RSRP in dBm, matching
    get_state), then neighbours: PCI 361 on 1700 at -78 dBm, and B7 PCI 237 on 2900 at "-127.10".
    On 3G the same B7 cell was printed as "-012.70": the 3G-mode format looks broken (probably -127 dBm),
    which also explains why the modem never took that B7 cell: it is barely there.
  - active_band_get gives the LTE band number (3), 0 on 3G.

## Version 0.4.0: Cells page (2026-11-02)

- SINR on LTE now comes from modem2 `snr` (sinr is always 0 in this firmware); test uses the real LTE output.
- Cells page (`#cells`), all passive reads that never interrupt the connection:
  - nearby LTE cells from $QCRSRP? + $QCRSRQ? every radio round (4 s) on /dev/smd8; band and downlink MHz
    from the EARFCN (TS 36.101 table: B1-B5, B7, B8, B12-14, B17, B20, B25, B26, B28, B38, B40, B41, B46, B48,
    B66, B71); a cell is kept for 60 s after it was last reported. The 3G print format is fixed up:
    an impossible RSRP (> -44) or RSRQ (> -3) that becomes plausible times ten is scaled and shown with "≈".
  - serving cell: band, MHz, EARFCN, PCI (first $QCRSRP? entry on LTE), cell ID, eNB ID = ID >> 8 and
    sector = ID & 0xFF (LTE), RNC = ID >> 16 and CID = ID & 0xFFFF (3G), MCC/MNC from modem2 "oper id".
    TAC is not available read-only: +CEREG?/+CREG?/$CREG? answer "0,1" (n=0) and modem2 prints lac:[0] on LTE;
    it would need AT+CEREG=2, a modem setting change, so it is shown as "Not reported".
  - CellMapper: no official description of the map URL parameters; only a third-party example with
    MCC/MNC/type/latitude/longitude, and the eNB is searched for on the site. So no link, only a hint.
  - Find all networks: results as a table (operator, network type, status, MCC MNC), and a confirmation
    dialog first (internet drops for 1-3 minutes). Same AT+COPS=? as before.
  - connection event log (300 in memory): network type, band, serving cell, registration, data session,
    IP address. Times come from Go's monotonic clock, so they survive the 1970 -> real time jump.
- Fixed: after the reboot the kernel had no IPv4 address on rmnet_data0 while ccm2 reported 10.58.8.156,
  so the panel would have said "No internet". The data session state and IP now come from ccm2.
- Live check on the router (read-only, /tmp, -password), LTE B3: band B3 1855.0 MHz, EARFCN 1700, PCI 80,
  eNB 560215 sector 31, MCC 255 MNC 01, RSRP -68..-70, RSRQ -8, SINR 20; neighbour PCI 73 on 1700 at -76.6.
  CPU: router 0.8% without the panel, 3.4% with it (panel ~0.53%, the vendor tools it starts ~1.8%), RSS 8.9 MB.

## TAC via AT+CEREG=2 (2026-11-02, with my OK)

- Sent `AT+CEREG=2` on /dev/smd8 only. Before: `+CEREG: 0,1` on smd8 and smd7. After: both ports answer
  `+CEREG: 2,1,"C95","88","88C571F",7`, so on this modem the setting is global, not per port.
- The 8800L adds a field: n, stat, TAC "C95" (= 3221, the same number as the 3G LAC here), "88" (probably
  RAC), cell ID "88C571F" (= 143415071, matches modem2), AcT 7. regAreaCell() handles both layouts.
- The panel reads AT+CEREG? in the passive cell poll and shows the TAC in decimal (as CellMapper uses it)
  with the hex value. Live: TAC 3221 (hex C95).
- Whether n=2 survives a reboot: not tested yet (needs a reboot, ask first).

## Device screen reconnaissance (2026-11-02, read-only)

- Display: /dev/fb0, 320x240, bits_per_pixel 18, stride 960 = 3 bytes per pixel. One read of /dev/fb0
  decoded correctly as **RGB666 packed big-endian into 3 bytes**: value = b0<<16 | b1<<8 | b2,
  R = bits 17-12, G = 11-6, B = 5-0 (white = 03 FF FF). No /sys/class/backlight; only /sys/class/leds/blue_led.
  fb0 blank = 0. Brightness is a devui setting (80).
- Input: event0 qpnp_pon (power key), event1 gpio-keys (one key), event2 cyttsp5_mt (Cypress touch,
  multitouch ABS). All root-only (crw-------).
- devuiappd (pid ~1079, started by /opt/nvtl/bin/nvtl_devui.sh from S50devui3) is a Qt 5.5 app:
  QT_QPA_PLATFORM=linuxfb, QT_QPA_EVDEV_TOUCHSCREEN_PARAMETERS=/dev/input/event2 (no grab option), fonts in
  /opt/qt5/lib/fonts. It holds /dev/fb0, /dev/input/event2 and /dev/tty0. It links QtDeclarative, i.e.
  QtQuick 1.1 (QML 1). **The QML is compiled into the binary** (HomePage.qml, InfoPanel*.qml... appear in
  `strings`); no .qml/.qrc/.rcc files on disk. Changing the stock screens means patching the binary: no.
  devuid (separate daemon) holds no display device; it is the settings/framework side (devui_cli talks to it).
- Stock UI language on this unit: Ukrainian. Home screen: signal + "4G LTE", battery, operator, Wi-Fi,
  devices, Wi-Fi name/password, Menu and Help buttons.
- mifi_display_png <prefix> [count] [usec]: draws PNG frames straight to /dev/fb0 (boot animation, 65 frames,
  run by /etc/rcS.d/S50_mifi_display_animation before the UI). sua_display_png.sh uses mifi_upi_disp for the
  update screens in /opt/nvtl/display/fota. Not run.
- **watchdogd: devuiappd is registered** (get_app_list has "devuiappd" and "devui_main_thread"); watchdog
  enabled, interval 60 s, padding 60 s; on a missed keep-alive it logs the app and reboots the router.
  So stopping or SIGSTOP-ing devuiappd without keep-alives reboots the router after about 1-2 minutes.
  watchdog_cli has application_register (register/unregister) and application_send_keep_alive.
- devui_cli getters: 6 info panels (Network, Messages, UniversalCharging, MiFiSharing (default), DataUsage
  enabled; Notifications disabled); screen timeout 30000 ms; display_setting_enabled 1; configurations include
  Brightness 80, colours, LowBatteryPercentage 15, DefaultAPN VZWINTERNET; 44 feature flags (audio alerts,
  ShowIpAddress, ShowAPNName...). Setters exist for timeout, display enabled, info panel display settings.

## Version 0.5.0: screen mirror and screen designer (2026-11-02)

Decisions: CEREG=2 is sent by the panel at startup (rule 3 exception in CLAUDE.md), no separate reboot test.
Screen: option A (mirror), then D (own screen on demand), built towards a widget-based screen designer.

- AT+CEREG=2 is sent once when the panel starts (EnableTAC), retried while the port is busy.
- Fonts: no dependency needed. Terminus 4.49.1 (SIL OFL 1.1) BDF files were downloaded from SourceForge
  (sha256 d961c1b7...ef79) and converted by tools/fontgen into fonts/*.bin: 6x12, 8x16, 8x16 bold, 12x24 bold,
  16x32 bold, 332 glyphs each (Latin, Latin-1, Ukrainian/Russian Cyrillic, arrows, ≈, −), 58 KB in all.
  Sizes "xxl" and "huge" are the 24 and 32 px fonts doubled.
- render.go: one Canvas for both outputs: PNG for the web preview and RGB666 bytes for /dev/fb0, so the
  preview is pixel-exact. Framebuffer decoding for the mirror (18 bpp verified; 16/32 bpp the usual way).
  Tested against real framebuffer pixels (white 03 FF FF, black, grey 03 CF 3C = 243,243,243).
- Widgets: metric (auto = RSRP on LTE, RSCP on 3G), band and cell, signal bars, speed, traffic, battery,
  clock (the router keeps UTC: "offset" option, filled from the browser's time zone), operator, devices,
  signal chart, antenna aiming (value, distance from the best, bar with best marker), data session/IP, text.
  Several pages per layout; default layout: Signal, Aiming, Internet.
- Web "Screen" page: live mirror (read every 2 s while open; about 56 ms per read on the router), designer
  on a 2x stage with snap to an 8 px grid, drag to move, corner handle to resize, arrow keys, per-widget
  options, live server-rendered preview (35-45 ms on the router), save, download and load JSON, default layout.
  Layouts are kept in memory; -layout-dir (e.g. /data/mifi-panel) keeps them on flash, not used yet (needs my OK).
- Live check (read-only): the mirror shows the real stock home screen with correct colours; previews on live
  LTE data (B3 1855.0 MHz, RSRP -69 excellent). Nothing was written to the screen.
- Step 5 done with my OK (2026-11-02 17:42 router time): `mifi-panel -test-frame 5s` saved /dev/fb0, wrote
  the test frame, waited 5 s and wrote the saved screen back; devuiappd kept running (same pid). A read of
  /dev/fb0 2.5 s in matched the expected frame exactly (0 of 76800 pixels differ: the stock UI did not repaint
  anything in that time); a read after the test showed the stock home screen again. Whether the LCD shows the
  colours right is for my eyes (red, green, blue, yellow, cyan, magenta bars).
- **But the LCD did not change.** I watched the screen during two more runs (15 s via write(); 20 s via mmap,
  then mmap + FBIOPAN_DISPLAY, which returned EINVAL): nothing on the display, although /dev/fb0 memory held
  the test frame. Reason (read-only checks): the panel is a Sitronix ST7789V2 (dmesg: "nvtl_lcd: Chip ID matched
  with sitronix,st7789_v2") with its own memory, driven by Inseego's nvtl_lcd driver (/sys/devices/79a2000.lcd:
  on, orientation, pattern_update, reg_addr, reg_value). /proc/kallsyms shows nvtl_lcd_ioctl and
  nvtl_lcd_fb_update and no deferred I/O or refresh thread: the panel is updated only on request.
  Disassembling the stock mifi_display_png (copied to the computer, not run) shows how: mmap the framebuffer,
  write the pixels, then ioctl(fd, 0x9999, &rect) with rect = int32 {x, y, width, height} = {0, 0, w, h}.
  The Qt linuxfb plugin only uses standard fb and tty ioctls (0x4600, 0x4602, 0x4611, KD*), so devuiappd
  probably makes the 0x9999 call itself. -test-frame now uses mmap + ioctl 0x9999.
- **Confirmed by eye (19:49 local): the test frame appeared on the LCD for 15 s; colours right (red, green,
  blue, yellow, cyan, magenta, black, white bars, grey steps), text incl. Cyrillic readable, the stock
  screen came back with no artefacts.** So: RGB666 big-endian packing is right, and drawing = mmap write +
  ioctl(0x9999, {0, 0, 320, 240}). devuiappd kept running (pid 1079) through all test frames.
- Side note: before that run the router's USB re-enumerated (now RNDIS + ADB + ACM + HID) without a reboot,
  and the local adb server stopped seeing it until `adb kill-server`.
- My OK for keeping screen layouts on flash in /data/mifi-panel/ (-layout-dir /data/mifi-panel); the file is
  written only when Save is pressed. Not used yet.
- Still to do, with my OK for the first live trial: step 6 ("Show on router": pause devuiappd with watchdog
  keep-alives, EVIOCGRAB on the touch device, failsafe, first 30 s trial together).

## Step 6: Show on router (2026-11-02)

- showscreen.go. Start order: save the current frame (memory + /tmp/mifi-panel-screen.saved), start the
  failsafe, grab the touch device (EVIOCGRAB on /dev/input/event2), turn the LCD on if it was off
  (/sys/devices/79a2000.lcd/on), SIGSTOP devuiappd, then draw the chosen page every second (mmap + ioctl 0x9999)
  with live data. Tap = next page, hold 1.5 s or power key (event0, read without grabbing) = back, or the
  duration ends (10 s .. 10 min). End: saved frame back on the panel, release the touch grab, LCD state back,
  SIGCONT devuiappd, tell the failsafe (SIGUSR1).
- Failsafe: the same binary with -screen-failsafe pid,deadline,lcd, started with setsid (own session). It
  sends watchdog keep-alives for "devuiappd" and "devui_main_thread" every 10 s (watchdog_cli
  application_send_keep_alive, name on stdin; added to vendorAllowed). At the deadline (duration + 15 s) it
  restores the saved frame and LCD state itself if the panel never said it finished, then SIGCONTs
  devuiappd (only if that pid still is devuiappd). If the panel dies, the kernel drops its touch grab.
- API: POST /api/screen/show {layout, page, seconds}, POST /api/screen/stop, GET /api/screen/session.
  UI: "Show for" + "Show on router" (with a confirmation that explains what happens) + Stop + live status.
  Demo: a session renders pages for the mirror and switches pages every 5 s.

### First live trial (20:04 local, with my OK, 30 s requested)

- Panel from /tmp with -password and -layout-dir /data/mifi-panel. Show started 20:04:46 with the Signal page.
- Watched every 3 s over adb: devuiappd (pid 1079) state T (stopped) during the show, one failsafe process,
  LCD on = 1. The page went from 1 to 3 (two taps registered), and the session ended by itself after about 9 s
  with "The stock screen is back" and no error: devuiappd back to state S (same pid), failsafe gone,
  /tmp/mifi-panel-screen.saved removed, router not rebooted (uptime 2:21). LCD "on" read 0 right at the end
  (the saved state was restored) and 1 again 3 s later (devuiappd).
- What I saw: the page appeared, taps switched pages, no artefacts; tap-and-hold ended the show as designed,
  then the screen went black. Cause: the LCD was already off when the show started (the stock 30 s timeout
  ran out while I was getting ready), the panel turned it on and restored "off" at the end. Now an exit from
  the router itself (long press, power button) leaves the screen lit and devuiappd switches it off later on
  its own timeout; time-up and Stop from the web still restore the previous state.
- The end reason was not recorded, so the session status now has "reason" (tap / long press on the screen /
  power button / time is up / stopped from the panel), also shown in the UI and the log.

### Second live trial (20:24 local)

- After the first trial the stock screen stayed black until I pressed the power button (the restored "off").
- Second run, Signal page: devuiappd stopped (T) with one failsafe during the show; I held my finger on the
  screen 8 s in; the session ended with reason "long press on the screen" (in the status and the panel log),
  the stock screen stayed lit (LCD on = 1) and devuiappd switched it off by itself about 12 s later.
  devuiappd back to S (same pid 1079), failsafe gone, no reboot (uptime 2:40).
- Show on router is working: drawing, page switching by tap, exit by long press, the failsafe and watchdog
  keep-alives, LCD state handling. Not yet tried live: exit by the power button, exit at time-up, the
  failsafe taking over after a panel crash.

## Version 0.6.0: widget palette (2026-11-02)

- widgets.go: one list of widget metadata (type, name, one-line description, options, minimum size, default
  size, S/M/L presets with the text size that suits each; devices has S/M, aiming S/L). Validate now grows
  widgets to their minimum size. GET /api/screen/widgets serves the list.
- Thumbnails: GET /api/screen/widget-preview?type=&size=&v=<version> renders the widget alone with the
  same renderer, cropped to its box, at 1x, on fixed sample data (LTE B3, RSRP -82, a fixed history curve,
  12:34), so they never change. Cached in memory after the first render and in the browser for a day
  (the version in the URL refreshes them on upgrade). On the router: first render 22-39 ms, cached ~4 ms.
- Palette: the dropdown is gone. Each card is a real button ("Add <name>, <w> by <h> pixels") with the
  thumbnail (shown at 2x when it fits the column, else 1x: integer scales only, pixelated), name, description,
  "W×H px", a tiny 320x240 outline with the area filled, and S/M/L switches that update the thumbnail.
  Hover or keyboard focus shows a dashed ghost on the canvas at the next free spot (first non-overlapping
  place on the 8 px grid, row by row; "no free space, it will overlap" when there is none). Press = add at
  the ghost; drag the card onto the screen = add where dropped (snapped). On phones the palette is a
  scrollable list (cards use touch-action: pan-y, so there pressing is the way to add). Resizing on the
  canvas respects the minimum size.
- Aiming widget: the "below the best" line shrinks its font to fit narrow boxes.
- Live check on the router (read-only apart from the preview): 13 widget types; CPU busy 3.5% with the panel
  idle, 7.0% with the Screen page open (most of it is the mirror every 2 s and the live preview every 3 s; the
  palette only costs its first thumbnails). Panel RSS 9.6 MB.
- My layout with signal bars was saved from the phone: /data/mifi-panel/screen-layout.json now exists on flash.
- adb lost the router twice after USB re-enumerations without a reboot; `adb kill-server` fixes it.

## Data catalog survey (2026-11-05, read-only, no code changes)

Result: `docs/data-catalog.md` (every value with source, example, units, availability, cost, privacy, and
whether the panel shows it; every AT command and vendor subcommand classified; top-20 list of values to add)
and masked raw outputs in `testdata/raw/` for parser tests. The router had just rebooted (uptime 2 min at the
start); LTE B3, Vodafone UA. Survey scratch files lived in `/tmp/cat` on the router and were removed.

- AT: `AT+CLAC` regenerated (240 commands). 186 queried with read forms only (386 queries, 309 OK), 54 never
  sent (actions, setters, SIM access, `+COPS=?`/`$QCCOPS=?`/`$QCCSGCOPS=?` scans, `$QCMRUC`/`$QCACQDBC`/`$QCCLR`
  clears, `+CMGL`/`+CMGR` which mark SMS read). Survey helper: one fd on /dev/smd8, poll for the final result
  code, hard `timeout -t`.
- **`AT+CEREG` n=2 does not survive a reboot** (`+CEREG: 0,1` after boot): the panel's startup `AT+CEREG=2` is needed.
- modem2 `lac` showed 3221 on LTE this time (0 on 2026-11-02); `enabled_tech_get` now 31 (was 18). Not reliable.
- **Firewall once LTE is up: iptables INPUT policy DROP**, accept br0 and lo, REJECT new connections to the WAN IP
  on rmnet_data0 (router2d adds this; the "all ACCEPT" in CLAUDE.md was before LTE). IPv6 is all ACCEPT but off.
- **Message bus**: 44 channels (`msgbus_cli print_channels`, `MsgBusGet` takes the channel on stdin, 20 ms).
  `modem2.data_signal_change` is the get_signal struct in binary (offsets in the catalog), with a timestamp.
  Subscribing is possible (not tried): a cheaper, event-driven source for later.
- **Panel bug found**: sys.go reads `power_supply/*/temp`, but bq27520 exposes `temp_ambient` (0.1 °C), so the
  battery temperature never shows. Also available: voltage_now, current_now, dsm `power_on_cause`, `power_state`.
- Temperatures: tsens_tz_sensor0..4 = 35-38 °C (0.1 °C units), xo_therm 33 °C; ambient/PA thermistors read -40
  and mdm_case 125: not fitted. CPU is pinned at 1.4 GHz (governor performance).
- `dmdb.db` (plain sqlite, read a copy in /tmp; sqlite3 3.16 has no `-readonly`): 533 data sessions since first
  use (start/end, IP, 3G/LTE), 2497 per-client usage rows, 24 known devices. Recent sessions have RX bytes = 0
  (probably IPA offload). `get_data_usage_statistics`: lifetime tx 24.7 GB, rx 399.7 GB.
- SMS: 20 unread of 120 in the inbox; `sms_cli get_list` gives headers; the sqlite files are encrypted;
  `sms_mo_supported 0` (no sending). `sms_cli read` not run (may mark read).
- GPS: privacy policy not accepted, so `get_last_fix` refuses; mode XTRA; gpsd still downloads XTRA data over LTE.
  A stock NMEA TCP server exists (port 11010, off).
- Wi-Fi: Broadcom BCM43596; `wl` gives per-client RSSI (`wl -i wlan0 rssi <MAC>`), `sta_info`, `chanim_stats`,
  noise; `iw` gives nothing. hostapd control sockets are in /var/run/hostapd.
- Stock web server: answers only with `Host: my.jetpack`; `/srv/status` (39 status-bar fields) and
  `/srv/notification` need no login; pages need login and show nothing beyond the CLIs.
- Secrets seen: `webui_cli get_login_settings` prints the web admin password in plain text, and it equals the
  primary Wi-Fi key; `wifi_cli get_ap_profile` prints both Wi-Fi keys. Masked everywhere.
- Surprises: `cdra_cli server_upload_status` is a setter (asks for a status to store; the empty input was rejected,
  nothing changed). `wifi_cli get_band_status` asks for a state "to wait for". `diag_read` is not a diag logger but
  the fuel-gauge/NV helper.
- Diag: logging mode 0 (USB), no MD session, no DCI clients; the diag USB interface (MI_03) has no driver on this
  computer, so SCAT/QCSuper could capture from the computer with nothing started on the router. `diag_mdlog`
  exists on the router but no mask files. Proposal in the catalog, waiting for my OK.
- adb lesson: `adb shell '(setsid cmd ... &)'` lost the process when the adb shell exited at once; adding
  `; sleep 1` inside the same adb command fixed it.

## Resume here (state on 2026-11-02, end of the v0.6.0 session; data catalog added 2026-11-05)

### Where things are

- Code: v0.6.0, committed to git; remote origin = git@github.com:Neit602/ModemWebInterface.git.
  Push only when I ask. Never commit probe*.txt or dist/ (router serial, MACs, client names; in .gitignore).
- Router: Vodafone UA SIM on LTE B3 (B3 enabled in the band preference, persistent; stays enabled).
  AT+CEREG=2 is sent by the panel at startup. Nothing is installed persistently except
  /data/mifi-panel/screen-layout.json (my saved screen layout; /data/mifi-panel/ is approved for layouts).
- The panel runs only from /tmp (RAM): after a reboot or power loss it is gone and has to be started again.
  No autostart yet.
- This computer gets its internet over another Wi-Fi (OLAX), not through the MiFi.

### How to run the panel on the router (the way it was done in this session)

```
sh build.sh                                   # vet + tests, builds dist/mifi-panel (ARMv7) and dist/mifi-panel-local
adb shell "pkill -x mifi-panel"               # stop an old copy if it runs
adb push dist/mifi-panel /tmp/
adb shell "chmod +x /tmp/mifi-panel; (setsid /tmp/mifi-panel -password <PW> -layout-dir /data/mifi-panel </dev/null >/tmp/panel.log 2>&1 &)"
adb forward tcp:8080 tcp:8080                 # then http://localhost:8080, or http://192.168.1.1:8080 over the MiFi Wi-Fi
```

- Login: user admin, password = the -password value. Generate at least 12 characters and check its length
  before telling me (once a filter left only 3 characters and I could not log in). Never write it into the repo.
- adb lessons: start background processes with `(setsid cmd </dev/null >log 2>&1 &)`, not `cmd &`;
  stop with `pkill -x` (pkill -f matches its own shell); after a USB re-enumeration run `adb kill-server`;
  adb forward can disappear, so re-add it rather than trusting it.
- Local demo: `dist/mifi-panel-local -demo`, or open web/index.html with `?demo=1` / `?demo=3g`.

### Open items, in order

1. Not tried live yet, only together with me: Restart data session; Show on router exits by the power button
   and at time-up; the failsafe taking over after the panel is killed mid-show.
2. Autostart plan (persistent location such as /data/mifi-panel or /opt, init script in /etc/rc3.d, password
   handling, full uninstall), proposed first and installed only with my OK (rule 2).
3. Possible CPU saving: the mirror on the Screen page reads /dev/fb0 every 2 s (7.0% router CPU with the page
   open vs 3.5% idle); it could refresh only when the picture changes.
4. IPA offload undercount check (speed test while comparing rmnet_data0, wlan0/1 and /sys/kernel/debug/ipa).
5. Roadmap in CLAUDE.md: Telegram bot with power-outage alerts and SMS forwarding, GPS signal logger,
   VPN, DNS ad blocking.
6. From the data catalog (2026-11-05): waiting for my OK on `modem2_cli efs_read` of the listed paths and on the
   diag step (SCAT on the computer over USB). Next features: the top-20 list in `docs/data-catalog.md`
   section 17 (session history page, battery voltage/current/temperature with the `temp_ambient` fix,
   power-on cause, per-client Wi-Fi signal, reject cause, network time zone...).
   `docs/data-catalog.md` and `testdata/raw/` are not committed yet.
