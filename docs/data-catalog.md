# Data catalog: everything the MiFi 8800L can tell us

Surveyed 2026-11-05 on this unit (firmware MiFiOS2 2022-03-16, modem 1.22.11), Vodafone UA SIM on LTE B3,
EARFCN 1700, router on USB power, 2 Wi-Fi clients. Everything was read-only: AT read forms (`?`, `=?`) and
report-only commands, vendor CLI getters, message bus reads, GET requests to the stock web server, and
`/proc`, `/sys` and tool output. Nothing was installed, nothing was left running, and `/tmp/cat` (the survey
scratch folder) was removed from the router afterwards.

Raw output (masked) is in `testdata/raw/` for future parser tests:

| File | What |
|---|---|
| `at-clac.txt` | `AT+CLAC` list (240 commands) |
| `at-plan.txt`, `at-not-run.txt` | the 386 queries that were sent, and the 54 commands never sent, with the reason |
| `at-read-forms.txt` | reply to every query, with `@ms` = time in the survey helper |
| `vendor-plan.txt`, `vendor-getters.txt` | 237 vendor CLI getters, empty stdin, with `time -p` and exit code |
| `vendor-plan-prompted.txt`, `vendor-getters-prompted.txt` | 95 getters that prompt, fed with explicit input |
| `msgbus.txt` | `MsgBusGet` on all 44 message bus channels |
| `linux-wifi.txt` | sysfs, `/proc`, iptables, conntrack, `ip`, dmesg, log listing, `wl`, `hostapd_cli`, `iw` |
| `stock-webui.txt` | `/srv/status` and `/srv/notification` JSON, HTTP status of the pages |
| `diag-debugfs.txt` | `/sys/kernel/debug/diag/*` (diag driver state) |

Masking (same rules everywhere, length-preserving so binary dumps stay valid): IMEI keeps the 8-digit TAC
(`99001687` + zeros), IMSI keeps MCC+MNC (`25501` + zeros), ICCID keeps `893800`, factory ID `fid` is zeroed,
MAC addresses become `02:00:00:00:00:NN` (one number per real MAC, consistent across files, also inside
message bus hex dumps), Wi-Fi keys and the web admin password become `x`, client host names become `host-N`,
the SSID suffix becomes `-0000`, link-local IPv6 becomes `fe80::200:ff:fe00:1`, the WAN IP keeps its first and
last octet (`10.11.11.109`), public IPs become `11.111.11.11`. No SMS text, phone number or GPS position was
collected (none available or not read).

Legend for the tables:

- **When**: `LTE`, `3G`, `always`, `GPS on`, `client` (needs a Wi-Fi client).
- **Cost**: wall time of one call on the router as measured; vendor CLIs take about 20-40 ms wall and 20-30 ms
  CPU each (they fork a process), message bus reads 20 ms / 10 ms, sysfs and `/proc` reads well under 10 ms.
  AT figures include the survey helper (it polls every 50 ms and drains 100 ms), so ~170 ms is the floor of the
  method, not of the modem.
- **Privacy**: `public` (operator/network facts), `device` (identifies this unit), `personal` (identifies
  the user or their devices), `secret` (passwords/keys).
- **Panel**: whether mifi-panel 0.6.0 shows it: `yes`, `partly`, `no`.

## 1. Sources at a glance

| Source | How | Count | Typical cost | Notes |
|---|---|---|---|---|
| AT on `/dev/smd8` | read forms only | 240 commands: 186 queried (386 queries, 309 answered OK), 54 never sent | < 0.2 s | most 3GPP signal commands are useless here; `$QC*` ones work |
| `modem2_cli` | getters | 77 (+ 43 band reads) | 30-40 ms, `ca_get_enabled` 1.5 s, `check_lte_ca_status` 1.0 s | main radio source; no Tx power, no SINR in `sinr`, radio temp is garbage |
| other vendor CLIs | getters | 160 | 20-70 ms | `cumclient` is not running (12 s timeout on `is_connect_to_server`) |
| message bus | `msgbus_cli MsgBusGet` | 44 channels | 20 ms / 10 ms CPU | binary structs, timestamped; event-driven source for later |
| stock web server | GET | 2 JSON endpoints without login | 40-90 ms | pages need login; same data as the CLIs |
| sysfs / `/proc` | cat | ~400 files | < 10 ms | battery, thermal, flash wear, CPU, network counters |
| Wi-Fi (`wl`, `hostapd_cli`) | Broadcom `wl` getters | 17 per interface + per client | 10-20 ms | per-client RSSI and rates; `iw` gives nothing (Broadcom full-MAC) |
| sqlite `dmdb.db` | read of a copy in `/tmp` | 8 tables | — | session history, per-client usage; SMS databases are encrypted |
| Qualcomm diag | not started | — | — | see section 14, proposal waiting for your OK |

## 2. Signal

| Value | Meaning | Source | Example | Units / decoding | When | Cost | Privacy | Panel |
|---|---|---|---|---|---|---|---|---|
| RSRP | LTE reference signal power of the serving cell | `modem2_cli get_state` `rsrp:[...]`; first entry of `AT$QCRSRP?`; msgbus `modem2.data_signal_change` @36 | -72 | dBm; 0 = not available | LTE | 30 ms | public | yes |
| RSRQ | LTE signal quality | `get_state` `rsrq`; `AT$QCRSRQ?`; msgbus @28 | -13 | dB | LTE | 30 ms | public | yes |
| SINR | LTE signal-to-noise | `get_state` **`snr`** (the `sinr` field is always 0); msgbus @32 | 13 | dB | LTE | 30 ms | public | yes |
| RSSI | total received power in the channel | `get_state` `rssi`; msgbus @8 | -38..-42 | dBm (wideband) | always | 30 ms | public | yes |
| Ec/Io | 3G pilot quality | `get_state` `ecio`; `AT$QCSQ` field 2; `get_diag_info` `Ec/Io: [UMTS]` | -4 (3G) | dB | 3G | 30 ms | public | yes |
| RSCP | 3G pilot power | computed RSSI + Ec/Io; `AT$QCSQ` field 1 | -62 (3G) | dBm | 3G | — | public | yes |
| 3G block error rate | share of corrupted 3G blocks | `get_diag_info` `Error Rate: [UMTS]:2%` | 2% (3G) | % | 3G | 50 ms | public | no |
| Signal bars | stock 0-5 bars | `get_state` `bars`; `/srv/status` `statusBarSignalBars`; `AT+CIND?` field 2 | 4-5 | 0..5 | always | 30 ms | public | partly (panel draws its own bars from RSRP) |
| `$QCSQ` on LTE | 3G-format signal line | `AT$QCSQ` | `-38,-2,0,0,-36` | RSCP, Ec/Io, SINR, ?, RSSI by its `=?`; on LTE the numbers look like RSSI, meaning unclear | 3G | < 0.2 s | public | 3G only |
| Neighbour LTE cells | PCI, EARFCN, RSRP/RSRQ of every heard cell | `AT$QCRSRP?`, `AT$QCRSRQ?` | `080,1700,"-071.90"` | first entry = serving cell on LTE; 3G-mode print is ×10 off | always | < 0.2 s | public | yes |
| `+CSQ`, `$CSQ` | 3GPP signal | `AT+CSQ`, `AT$CSQ` | `99,99` | always "unknown" on this firmware | — | — | public | no (useless) |
| `$QCRSCP?`, `$QCECNO?` | WCDMA neighbour lists | AT | empty | empty on LTE and on 3G | — | — | public | no |
| Tx power | how hard the modem transmits | `get_state` `tx power:[0]` | 0 | always 0 here: not reported; only diag has it | — | — | public | no |
| Radio temperature | RF temperature | `get_state` `radio temp[...]` | -1301287780 | uninitialised memory: not available | — | — | public | no |
| `get_diag_info` RSSI | "RSSI [0]" | `get_diag_info` | -125 on LTE | stale CDMA/3G field; ignore on LTE | 3G | 50 ms | public | no |

Notes: the `get_voice_signal` struct is all zeros (no voice service). `dbm`, `sid`, `nid`, `p_rev`, `femto` are
CDMA leftovers and stay 0.

## 3. Network, registration and cell identity

| Value | Meaning | Source | Example | Units / decoding | When | Cost | Privacy | Panel |
|---|---|---|---|---|---|---|---|---|
| Network type | LTE / HSPA+ / UMTS | `get_state` `tech:[10] (LTE)`; `AT$QCSYSMODE?` (`LTE`, `WCDMA - HSDPA and HSUPA`); `AT+COPS?` AcT 7/2; `^SYSINFO` sysmode 9 (LTE) / 5 (WCDMA); msgbus `modem2.service` | 10 | modem2 tech codes: 9 HSPA+, 10 LTE | always | 30 ms | public | yes |
| Registration | home / roaming / searching | `get_state` `reg state`; `AT+CEREG?`, `+CGREG?`, `+CREG?`, `$CREG?`; msgbus `modem2.registration_status` | 1 | 3GPP stat: 1 home, 5 roaming, 2 searching | always | 30 ms | public | yes |
| Service domain | circuit + packet or packet only | `AT^SYSINFO` field 2 | 3 | 0 none, 1 CS, 2 PS, 3 CS+PS | always | < 0.2 s | public | no |
| Roaming | roaming flag | `get_state` `roam`; `get_service_info`; `/srv/status` `statusBarRoaming` | 0 / `None` | 0 = home | always | 30 ms | public | no |
| Operator | name and MCC/MNC | `get_state` `oper name`, `oper id`; `AT+COPS?` (name printed twice); `get_oper_info` | Vodafone UA, 25501 | MCC 3 digits + MNC (length 2 from `sim_get_mnc_length`) | always | 30 ms | public | yes |
| Cell ID | serving cell identity | `get_state` `cell id`; `AT+CEREG?` with n=2 (hex); msgbus @140 | 143415071 | LTE: eNB = ID >> 8, sector = ID & 0xFF | always | 30 ms | public (rough location) | yes |
| TAC / LAC | tracking / location area | `AT+CEREG?` after `AT+CEREG=2`; `get_state` `lac` | 3221 (hex C95) | decimal; **modem2 `lac` gave 3221 on LTE this time** (0 on 2026-11-02), so it is not reliable alone | always | 30 ms | public | yes |
| `+CEREG` n after a reboot | whether the TAC setting survives | `AT+CEREG?` right after boot | `0,1` | **n=2 is lost on reboot**; the panel's startup `AT+CEREG=2` is needed every time | — | — | — | — |
| Band | LTE band in use | `active_band_get`; EARFCN from `$QCRSRP?` | 3 | 0 on 3G | LTE | 30 ms | public | yes |
| EARFCN, PCI | channel and physical cell ID | `$QCRSRP?` first entry | 1700, 80 | TS 36.101 table in cells.go | LTE | < 0.2 s | public | yes |
| `get_oper_info` "Freq band" | — | `get_oper_info` | 0 | always 0: useless | — | — | — | no |
| Network reject cause | why the network refused the last registration | `modem2_cli get_reject_cause_code` | 0 | 3GPP EMM/MM cause (e.g. 15 no suitable cells in TA); 0 = none | always | 30 ms | public | no |
| Last failure reason | modem's last call/attach failure text | `AT+CEER` | "No cause information available" | free text | always | < 0.2 s | public | no |
| Network time and zone | time from the network | `get_network_time`, `get_cached_time`; `AT+CCLK?`, `AT$CCLK?`; msgbus `modem2.time_change` | `26/11/05,13:22:11+08` | zone in quarter hours: +08 = UTC+2; modem2 prints `Time zone:[8]` and `DST:[0]` | after registration | 30 ms | public | no |
| Packet attach / contexts | attached, which contexts are up | `AT+CGATT?`, `AT+CGACT?` | `1`; `1,1 2,0 3,0` | context 1 = internet | always | < 0.2 s | public | no |
| Activity status | modem activity | `AT+CPAS` | 4 | 3GPP: 4 = call in progress (data) | always | < 0.2 s | public | no |
| Radio power | radio on / airplane | `AT+CFUN?`; `radio_is_enabled`; `ccm2_cli get_air_mode_enabled` | 1 / 1 / 0 | 1 = full | always | 30 ms | public | no |
| Manual selection info | manual network selection allowed / used / scan state | `modem2_cli mns_get_info`; `/srv/status` `statusBarMns*` | allowed 1, selected 0, scan 0 | flags | always | 30 ms | public | partly (Find all networks) |
| Last scan result | stock "manual network search" list | `mns_get_list` | "No scan results available" | only after a stock scan | — | 30 ms | public | no |
| Better-system reselection timers | how often the modem looks for LTE when it is on 3G | `modem2_cli bsr_get_timers` | BSR 180 s, max 180 s, **next LTE scan 300 s** | seconds | always | 40 ms | public | no |
| Most recently used bands | bands the modem used before, per RAT | `AT$QCMRUE?` | `0,4,"0,1,...,26","00000"` | index, RAT (4 = LTE), band list; not fully decoded | always | < 0.2 s | public | no |
| `^SYSCONFIG` | mode and acquisition order | `AT^SYSCONFIG?` | `2,0,1,2` | 2 auto, 0 auto order, roam 1, domain 2 | always | < 0.2 s | public | no |
| `+WS46`, `+CEMODE` | RAT family, UE mode | AT | 25; 2 | 25 = GERAN/UTRAN/E-UTRAN; mode 2 = CS/PS mode 2 | always | < 0.2 s | public | no |
| Dormancy | whether the data link is dormant | msgbus `modem2.conn_dormancy` (int32 @0); `get_diag_info` `Dormant State` | 1 / Dormant | meaning on LTE (RRC idle?) unverified | always | 20 ms | public | no |
| Carrier aggregation | CA active / bands | `check_lte_ca_status` | "Failure. returned code = 0" | no CA reported; `ca_get_enabled` and `ca_bands_get_enabled` return errors and garbage | LTE | 1.0-1.5 s | public | no |

## 4. Data session and IP

| Value | Meaning | Source | Example | Units / decoding | When | Cost | Privacy | Panel |
|---|---|---|---|---|---|---|---|---|
| Session state | connected / waiting / not available | `ccm2_cli get_curr_wan_stats` (stdin `1`), `get_curr_wan`, `get_conn_state`; `modem2_cli call_get_status` (stdin `1\n0\n`): `WWAN State:[5] (Connected)`; `/srv/status` `statusBarConnectionState`; msgbus `ccm2.wan_connection` | Connect | text / 5 = connected, 3 = idle | always | 40 ms | public | yes |
| Duration | time since the session came up | ccm2 `V4 Duration`; `/srv/status` `statusBarConnectionDuration` | 647 | seconds | connected | 40 ms | public | yes |
| WAN IPv4, mask, gateway | address from the operator | `call_get_status`; `AT+CGCONTRDP`; `AT+CGPADDR`; msgbus `ccm2.wan_stats` | 10.11.11.109/30, gw 10.11.11.110 | /30 point-to-point | connected | 40 ms | device | partly (IP only) |
| Operator DNS | DNS servers from the network | `router2_cli get_active_dns`; `call_get_status`; `AT+CGCONTRDP` | two IPv4 | — | connected | 40 ms | public | no |
| APN in use | access point name | `AT+CGCONTRDP` (`internet`); `+CGDCONT?` (profile 1 empty = network-assigned, 2 `ims`, 3 `sos`); `prof_get_tech` (stdin `0\n1\n`) | internet | — | connected | < 0.2 s | public | no |
| Active profile | which profile is used | `prof_get_act_tech` (stdin `0\n`); `AT$QCDEFPROF?` | idx 1 | — | always | 40 ms | public | no |
| IPv6 | IPv6 state | ccm2 `V6:[Waiting]`; `get_ipv6_enabled` 0; `call_get_status` stdin `1\n2\n` → Idle | off | — | always | 40 ms | public | no |
| Autoconnect | reconnect policy and LAN client count | `ccm2_cli get_wwan_ac`, `get_wwan_ac_state` | type 2 "when service is available", interval 10 s, LAN clients 2 | — | always | 40 ms | public | no |
| Call throttling | operator back-off after failures | `ccm2_cli get_call_throttle_state`; `modem2_cli get_call_throttle_status` | 0 / 0 | 1 = throttled | always | 40 ms | public | no |
| Session bytes | bytes in this session | ccm2 stats; `/srv/status` `statusBarBytesReceived/Transmitted/Total` | 4877 / 8665 | bytes; equals `/proc/net/dev` rmnet_data0 | connected | 40-90 ms | public | yes (from /proc) |
| Cellular data enabled | user switch | `ccm2_cli get_cell_data_enabled`; `/srv/status` `statusBarMobileDataEnabled` | 1 | — | always | 40 ms | public | no |
| PCO event | operator protocol config options (e.g. "out of data") | `modem2_cli get_pco_event` | value 0 | — | always | 40 ms | public | no |

## 5. Modem configuration (read-only views)

| Value | Meaning | Source | Example | Decoding | Cost | Panel |
|---|---|---|---|---|---|---|
| LTE band preference | which LTE bands the modem may use | `lte_band_get_enabled` (stdin = band 1..43), 43 calls | all 1 now (B3 enabled since 2026-11-02) | bands ≥ 44 cannot be read (rc 2) | 43 × 40 ms | no |
| Applied LTE bands | bands actually in effect | `get_diag_info` "LTE Band Class" | includes E_UTRA_BAND_3 | updates only after a modem restart | 50 ms | no |
| WCDMA band preference | 3G bands | `AT$QCBANDPREF?`; `get_diag_info` "CDMA Band Class" | I, II, V, VIII | — | < 0.2 s | no |
| Tech modes | enabled / active / supported | `enabled_tech_get`, `active_tech_get`, `get_sup_tech` | 31 / 31 / 79 | bit masks; **enabled was 18 ("UMTS LTE") on 2026-11-02, 31 now** | 30 ms | no |
| Roaming allowed | domestic / international | `roam_get_enabled`, `roam_get_intl_enabled` | 0 / 0 | — | 30 ms | no |
| World mode | — | `get_world_mode_enabled` | 1 | — | 30 ms | no |
| DRX | idle paging cycle | `AT$QCDRX?` | 0 | 0 = network default | < 0.2 s | no |
| SD config type 1 | system determination timer | `sd_config_get_setting` stdin `1` | 180 | seconds (other types: error) | 40 ms | no |
| PDN extras | per-profile limits | `pdn_get_ext_params` stdin `N\n0\n` | max conn 20, conn timer 300 | — | 40 ms | no |
| Profiles | APN, auth, DNS of each profile | `prof_get_tech`, `prof_get_pri_tech`, `prof_get_cust_tech`; `AT$QCPRFMOD?`, `$QCAPNE?`, `$QCPDPCFGE?`, `$QCPDPP?` | profile1 empty APN, profile2 ims, profile3 sos; no auth | username/password fields empty (would be secret) | 40 ms | no |
| PRI / carrier | Verizon config level | `get_info` `Pri Version:[151]`; `devui_cli get_configurations` `DefaultAPN VZWINTERNET`; `sim_get_carrier` UNKNOWN; `cc_cli *` all fail | — | — | 30 ms | no |
| CDMA / IMS / VoLTE / MIP | Verizon leftovers | `band_class_get_enabled`, `ehrpd_*`, `ims_*`, `volte_get_*`, `mip_*`, `1xrtt_*`, `cai_get`, `lbs_get`, `ddtm_get`, `emergency_get_mode` | zeros, errors or garbage | nothing useful in Ukraine | 40 ms | no |
| Carrier unlock | lock state | `get_carrier_unlock` | error, state 0 | — | 30 ms | no |
| Refurb / activation date | factory data | `get_refurb_info`, `get_act_date` | error, 1970 | — | 30 ms | no |

## 6. SIM and device identity

| Value | Source | Example (masked) | Privacy | Panel |
|---|---|---|---|---|
| IMEI | `ATI`, `AT+GSN`, `get_info` | 990016870000000 | device | no |
| IMSI | `AT+CIMI`, `get_imsi`, `get_info`, `get_diag_info` | 255010000000000 | personal | no |
| ICCID | `AT+ICCID?` (answers `$NWICCID:`), `sim_get_iccid`, `get_info` | 8938000000000000000 | personal | no |
| Own number | `AT+CNUM` (ERROR), `$QCMSISDN?` (empty), `get_info` `mdn` (empty) | not on the SIM | personal | no |
| SMS centre | `AT+CSCA?` | operator SMSC number | public | no |
| SIM state, PIN/PUK retries | `sim_get_status` / `sim_pin_get_status` (status 3 READY, retries 3, unblocks 10); `AT+CPIN?`; `AT$QCPINSTAT?`; `$QCSIMSTAT?`; msgbus `modem2.pin1_state`, `modem2.uim_state` | READY, 3, 10 | public | no |
| SIM type, apps | `AT$QCSIMT?` USIM; `AT^CARDMODE` 2; `AT+CUAD` AID list; `$QCSIMAPP?` | USIM | public | no |
| GID1/GID2 | `sim_get_gid1/2` | none | public | no |
| Phonebook | `AT+CPBS?` | SM, 1 of 500 used | personal (entries not read) | no |
| Firmware | `ATI`, `AT+GMR`, `get_info` | SDx20ALP-1.22.11 2020-04-13 | public | no |
| Hardware | `get_info` HW 4; `AT$QCHWREV` Part 333 v2.0; model MIFI8800L; maker OUI 0015FF | — | public | no |
| Factory ID | `get_info` `fid` | FZ000000000000 | device | no |
| Branding | `settings_cli get_branding_info` (sku 8800, `cute_version 2N`), `webui_cli get_site_title_info` | Jetpack MiFi 8800L | public | no |
| Wi-Fi chip | `wl revinfo`, `wl ver` | BCM43596 (chipnum 0xaa4c), driver 9.86.13 | public | no |

## 7. Power, battery and temperature

| Value | Meaning | Source | Example | Units / decoding | When | Cost | Privacy | Panel |
|---|---|---|---|---|---|---|---|---|
| Battery level | charge | `/sys/class/power_supply/bq27520-battery/capacity`; `dsm_cli get_state`; `/srv/status` | 100 | % | always | < 10 ms | public | yes |
| Battery status | charging / full / discharging | `bq27520-battery/status`; dsm `battery_state` | Full | text | always | < 10 ms | public | yes |
| External power | on mains/USB or on battery | `power_supply/usb/online`; dsm `power_state` (`online_usb` / battery), `charger_det` | 1 / online_usb | — | always | < 10 ms | public | yes |
| Battery voltage | — | `bq27520-battery/voltage_now`; dsm `battery_voltage` | 4277000 | µV | always | < 10 ms | public | no |
| Battery current | charge (+) or drain | `bq27520-battery/current_now`; dsm `battery_current` | 84000 | µA; sign on battery to be checked | always | < 10 ms | public | no |
| Battery temperature | — | `bq27520-battery/temp_ambient`; dsm `battery_temperature` | 284 / 278 | 0.1 °C. **The panel reads `temp`, which this battery does not have, so it never shows it** | always | < 10 ms | public | no |
| Charger | charge phase and health | `smb1351-charger/charge_type`, `health`, `charging_enabled` | Taper, Good, 1 | — | always | < 10 ms | public | no |
| USB supply | current limit | `power_supply/usb/current_max`, `voltage_max` | 500000, 5000000 | µA, µV | always | < 10 ms | public | no |
| Power-on cause | why the router started | dsm `power_on_cause` | power_button | text | always | 30 ms | public | no |
| Power button, display | state | dsm `power_button`, `display_state`; `/sys/devices/79a2000.lcd/on` | released, off, 0 | — | always | 30 ms | public | no |
| Thresholds | low / critical battery | `dsm_cli get_settings` (critical 1, low 25 for the battery pack); devui `LowBatteryPercentage 15` | — | % | always | 30 ms | public | no |
| Power save | deep sleep policy | `powersave_cli get_settings` | deep sleep after 300 s idle, wake every 7200 s | seconds | always | 30 ms | public | no |
| Modem view of battery | — | `AT+CBC` `2,100`; `AT$QCVOLT` `0` | — | modem does not see the battery | — | — | public | no |
| SoC temperatures | chip sensors | `/sys/class/thermal/thermal_zone5..9/temp` (`tsens_tz_sensor0..4`) | 370, 370, 363, 352, 377 | 0.1 °C (35-38 °C) | always | < 10 ms | public | no |
| Crystal temperature | `xo_therm_buf` | `thermal_zone0/temp` | 33 | °C | always | < 10 ms | public | no |
| Other thermistors | ambient, PA 1/2, case | `thermal_zone1..4` | -40, -40, -40, 125 | not fitted / not valid on this board | — | — | — | no |

## 8. System

| Value | Source | Example | Notes | Panel |
|---|---|---|---|---|
| Uptime, load, memory | `/proc/uptime`, `/proc/loadavg`, `/proc/meminfo`, `free` | 14 min, 1.15; 374 MB RAM, 113 MB free + 143 MB cache | | yes |
| CPU frequency | `/sys/devices/system/cpu/cpu0/cpufreq/*` | 1401.6 MHz, governor `performance`, never changes (time_in_state all at max) | no saving to read here | no |
| CPU idle | `cpu0/cpuidle/state*` | only WFI is used | | no |
| Per-process CPU | `top -b -n 1` (150 ms) | | for the panel's own cost | no |
| Flash wear | `/sys/class/ubi/ubi0/{max_ec,bad_peb_count,reserved_for_bad,avail_eraseblocks}` | max erase count 144, 2 bad blocks, 38 reserved, rootfs 1442 of 1484 blocks; `ubi1` = modem volume, max_ec 2 | storage health before autostart writes to flash | no |
| Disk space | `df` | | | no |
| Kernel log | `dmesg` (1255 lines) | | Wi-Fi start, USB, modem SSR messages | no |
| System log | `/opt/nvtl/log/system_log` (1.4 MB) + 3 rotated `.tgz`; `/var/log/messages`, `/var/log/debug_log` (RAM) | modem2d logs PLMN, LAC, cell ID changes | `logread` does not work: syslogd writes files | no |
| Watchdog | `watchdog_cli get_settings`, `get_app_list` | enabled, 60 s, 37 registered threads | | no |
| Crash data | `/opt/nvtl/log/crash`, `/data/tombstones/{modem,lpass}`, `/data/reboot-cookie` | empty, empty, `0` | modem crash dumps would appear in tombstones | no |
| Runlevel | `dsm_cli get_persistent_rl` | 3, not persistent | | no |
| Stock services | `*_cli is_running` | all running except cumclient | | no |

## 9. Wi-Fi and clients

| Value | Meaning | Source | Example | Units | When | Cost | Privacy | Panel |
|---|---|---|---|---|---|---|---|---|
| SSIDs, bands, channels | the two networks | `wl -i wlan0/wlan1 status`, `chanspec`; `hostapd_cli -p /var/run/hostapd -i wlan0 status` | primary 5 GHz ch 161/80 MHz (5805 MHz), guest 2.4 GHz ch 6 | — | always | 20 ms | personal (SSID) | no |
| Noise floor | background noise | `wl noise` | -89 (5 GHz), -92 (2.4 GHz) | dBm | always | 20 ms | public | no |
| Channel load | how busy the air is | `wl chanim_stats` | 5 GHz idle 100%; 2.4 GHz idle 91%, obss 1, glitch 961 | % of time | always | 20 ms | public | no |
| AP max rate | PHY rate of the AP | `wl rate` | 390 / 86.5 Mbps | Mbps | always | 20 ms | public | no |
| Tx power | AP transmit power | `wl txpwr1`, `wl curpower` | target 31.75 dBm (board/regulatory limits printed) | dBm | always | 20 ms | public | no |
| Country | regulatory domain | `wl country`; `wifi_cli get_settings` | US | — | always | 20 ms | public | no |
| Radio counters | retries, failures, CRC errors | `wl counters` | txretrans 3061, rxcrc 21 | counts since start | always | 20 ms | public | no |
| Client list | associated MACs | `wl assoclist`; `hostapd_cli all_sta`; `wifi_cli get_sta_list`; msgbus `wifi.wlan0.sta_list_updated` | 1 client on 5 GHz | — | client | 20 ms | personal | yes (from dmdb/leases) |
| Client signal | RSSI of each client | `wl -i wlan0 rssi <MAC>`; `sta_info` "per antenna rssi" | -66 | dBm | client | 20 ms | personal | no |
| Client link | last Tx/Rx rate, idle, connected time, retries, VHT/HT caps | `wl sta_info <MAC>` | 6-27 Mbps, idle 5 s, 305 s connected | — | client | 20 ms | personal | no |
| Client bytes (Wi-Fi) | per-client Tx/Rx | `wl sta_info` | tx 8797 B, rx 23151 B | bytes since association | client | 20 ms | personal | no |
| Device names / IP / interface | stock device database | `dmdb_cli get_connected_device_list`, `get_known_device_list` (24 known) | host-1, 192.168.1.x, WIFI Primary | — | always | 40 ms | personal | yes (connected list) |
| AP limits | max clients | `wifi_cli get_ap_settings` (5), `get_caps` (15 supported, channels, modes) | — | — | always | 40 ms | public | no |
| Wi-Fi keys | passwords of both networks | `wifi_cli get_ap_profile` stdin 1..4 | masked | — | always | 40 ms | **secret** | no (never show) |

`iw dev wlan0 station dump` prints nothing (Broadcom full-MAC driver); `wl` is the tool. A neighbour AP scan
(`wl scan` / `wl scanresults`) briefly takes the radio off-channel: **not run, needs your OK**.

## 10. Router, firewall and traffic counters

| Value | Source | Example | Notes | Panel |
|---|---|---|---|---|
| LAN settings | `router2_cli get_lan_settings`, `get_dhcp`, `get_dns_name`, `get_mtu` | 192.168.1.1/24, DHCP .2-.102, 24 h leases, `my.jetpack`, MTU 1428 | | no |
| Router features | `get_port_forwarding`, `get_port_filter`, `get_dmz`, `get_vpnpassthrough`, `get_http_redirect`, `get_v6_fw`, `get_manual_dns`, `get_ntp`, `get_ippt` | off, off, off, on, on, on, off, off, off | | no |
| Firewall | `iptables -L -v -n -x` | **with LTE up, INPUT policy is DROP**: accept br0 and lo, REJECT new TCP/UDP to the WAN IP on rmnet_data0; FORWARD DROP except br0↔rmnet; IPv6 all ACCEPT (IPv6 is off) | counters show blocked inbound attempts from the internet | no |
| Connection tracking | `/proc/sys/net/netfilter/nf_conntrack_count`, `_max`; `conntrack -L` | 8 entries (all local syslog) | destinations = visited sites: personal | no |
| Interface counters | `/proc/net/dev`, `/sys/class/net/*/statistics/*`, `ip -s -s link` | bytes, packets, errors, drops per interface | rmnet RX may undercount with IPA offload (open item) | yes (speed, totals) |
| TCP/UDP stats | `/proc/net/snmp`, `netstat` | retransmits, errors | | no |
| IPA offload | `/sys/kernel/debug/ipa/` (29 files listed, not read) | | for the undercount check | no |
| Lifetime traffic | `dmdb_cli get_data_usage_statistics` (stdin `\n\n\n-1\n`) | tx 24.7 GB, rx 399.7 GB (non-roaming) | since the database started | no |
| Session history | `dmdb.db` table `DataSessions` (read a copy) | 533 sessions since 1970-01-01 00:00:37: start/end, IP, bearer (108 × 3G, 425 × LTE), bytes | **RX bytes are 0 in recent rows** (probably IPA offload): use start/end, not bytes | no |
| Per-client usage | `dmdb.db` `UsageLogs` (2497 rows), `KnownDevices` (24) | MAC, client IP, session, bytes per client | personal | no |
| Traffic status | `dmdb_cli get_traffic_status`; `/srv/status` `statusBarTrafficStatus` | None | stock "traffic" indicator | no |
| Stock usage plan | `dua_cli get_data_usage_info` | invalid (Verizon plan service) | useless here | no |
| `lifetime_counters_get` | modem2 | error, 0 KB | useless | no |

## 11. GPS

| Value | Source | Example | Notes |
|---|---|---|---|
| Engine state | `gps_cli gps_status`; msgbus `gps.engine.status` | Disabled | |
| Privacy accepted | `gps_cli get_privacy` | 0 | `get_last_fix` refuses with 1001 until the privacy policy is accepted (`set_privacy`: a setting, needs your OK) |
| Last fix | `gps_cli get_last_fix` | none | position would be personal |
| Mode | `gps_cli get_mode` | XTRA | assistance data; the stock gpsd downloads `xtra3grcej.bin` over LTE with curl even with GPS off (seen in `ps`) |
| NMEA TCP server | `gps_cli get_nmea_tcp` | off, port 11010 | a stock NMEA stream exists; turning it on is a setting (roadmap 4) |
| Default on | `gps_cli get_active` | on | |

## 12. SMS

| Value | Source | Example | Notes |
|---|---|---|---|
| Unread count | `sms_cli get_unread`; msgbus `sms.list.unread`; `/srv/status` `statusBarSmsUnreadCount` | 20 | cheapest via msgbus or /srv/status |
| Message list | `sms_cli get_list` stdin `1` (Inbox), `0` (PreInbox) | 120 headers: id, Read/Unread, storage | Outbox/Sent: "feature is unsupported" (`sms_mo_supported 0`: no sending) |
| Message text | `sms_cli read <id>` | not run | may mark as read; ask before using (Telegram forwarding) |
| Storage | `AT+CPMS?` | `"ME",0,23` | the modem keeps nothing; smsd moves messages to Linux |
| Databases | `/opt/nvtl/data/sms/sms_inbox.db`, `sms_pre_inbox.db`, `sms_history.txt` | encrypted / binary | sqlite cannot open them |

## 13. Message bus (event source)

`/tmp/msgbus.socket`, served by msgbusd. Every channel keeps its last value and a timestamp; readers can also
subscribe (`MsgBus_Subscribe`, not run in this step). One `MsgBusGet` costs 20 ms wall, 10 ms CPU. Binary
values are little-endian C structs; `modem2.data_signal_change` uses the same layout as `get_signal`:
@0 tech, @4 roam, @8 RSSI, @12 dbm, @16 bars, @20 Ec/Io, @24 sinr, @28 RSRQ, @32 SNR, @36 RSRP, @40 Tx power,
@44 operator name (string), @108 MCC-MNC string, @140 cell ID string, @172 LAC/TAC string, @224 radio temp.

| Channel | Type | Example | Meaning |
|---|---|---|---|
| `ccm2.wan_connection` | int32 | 1 | WAN up |
| `ccm2.wan_stats` | 240 B | WWAN, state, bytes, ifname, IP, mask, gateway | data session stats (partly decoded) |
| `modem2.data_signal_change` | 228 B | LTE, RSSI -42, bars 4, RSRQ -13, SNR 13, RSRP -73 | signal struct, updated on every change |
| `modem2.operator_change` | 228 B | same struct | at operator/cell change |
| `modem2.conn_state` | 129 B | state 5, rmnet_data0, IPs, DNS | data call state |
| `modem2.conn_dormancy` | 12 B | 1 | dormancy flag |
| `modem2.registration_status` | int32 | 1 | home |
| `modem2.service` | int32 | 10 | tech |
| `modem2.roam_state` | 8 B | 0, 1 | |
| `modem2.device_state` | int32 | 5 | |
| `modem2.operating.mode` | int32 | 0 | online |
| `modem2.time_change` | 12 B | seconds counter, zone 8 | network time |
| `modem2.pin1_state`, `modem2.uim_state`, `modem2.non_pri_sim`, `modem2.plmn_mode`, `modem2.wifi_band_conflict` | small | | SIM and misc state |
| `dmdb.connected.device.list` | 4 B | 2 | connected client count |
| `dmdb.dus.traffic_status`, `dmdb.dus.report.statistics`, `dmdb.*.settings`, `dmdb.main.initialized` | int32 | | device database state |
| `hostapd.ap.enabled`, `hostapd.ap.sta_connected`, `wifi.sta_connected` | binary | interface + client MAC | Wi-Fi join events |
| `wifi.2ghz.enabled`, `wifi.5ghz.enabled`, `wifi.wlan0.if_up`, `wifi.wlan0/1.sta_list_updated` | int32 / 116 B | | Wi-Fi state and client lists |
| `sms.list.unread` | int32 | 20 | unread SMS |
| `gps.engine.status` | int32 | 1 | |
| `nvtl.ans.notifications`, `nvtl.dua.usage.info`, `su.broadcast`, `file_sharing.broadcast`, `usb.attached_to_bridge`, `led.ready`, `devui.ready`, `settings.ready`, `webui.ready` | various | | stock UI plumbing |

## 14. Stock web server

lighttpd on :80 answers only to `Host: my.jetpack` (or the LAN IP); anything else gets 307 to `http://my.jetpack`.

| Request | Login | Cost | What it gives |
|---|---|---|---|
| `GET /srv/status` | no | 90 ms | 39 status-bar fields: battery %, charging source, network, MCC-MNC, tech, bars, roaming, connection state and duration, session bytes, client counts (primary/guest/max), SMS unread, SIM status, GPS, Wi-Fi on, MNS flags, SD card |
| `GET /srv/notification` | no | 40 ms | `notificationsList` (empty) |
| `GET /gps/status/` | — | 170 ms | 406 without the browser headers; not pursued |
| `/fieldtest/`, `/internet/`, `/jetpackinfo/` and the other app pages | yes | 770 ms | login page. Their templates show `get_diag_info` (field test), session details and device info: nothing beyond the CLIs |
| `/srv/connect`, `/srv/disconnect`, every POST | — | — | actions, not called |

The stock admin password is readable in plain text with `webui_cli get_login_settings` and equals the primary
Wi-Fi key (both are the label default). Treat both as secret.

## 15. Not run, and why

| Item | Why | What it would give |
|---|---|---|
| `modem2_cli efs_read` | **waiting for your OK on the path list below** | raw NV/EFS items (band preference as stored, IMS config) |
| Qualcomm diag logging | proposal in section 16, waiting for your OK | Tx power, timing advance, CQI, MCS, BLER, RRC messages |
| `wl scan` (neighbour Wi-Fi) | takes the radio off-channel briefly | nearby APs, for channel choice |
| `MsgBus_Subscribe` | listen-only, but outside what this step allowed | push updates instead of polling |
| `ccm2_cli apps_call_state`, `dm_call_state` | rule 4 lists `apps_call_*` / `dm_call_*` as needing OK | state of the stock management APN calls |
| `modem2_cli mns_validate`, `validate_home`, `validate_apn` | "validate" may act | home network check |
| `sms_cli read` | may mark the message as read | SMS text |
| `router2_cli dns_lookup` | sends a DNS query over LTE | — |
| `webui_cli get_external_uri_data` | may fetch from the internet | — |
| `emd_cli log`, `router2_cli log`, `wifi_cli log` | write internal state to the system log | internal state dumps |
| `AT+CLIP?`, `+CDIP?`, `+CLIR?` | sent, but no answer within 1 s (they ask the network) | caller ID settings, irrelevant |
| `cdra_cli server_upload_status` | **was run once: it is a setter** (asks "Enter the server upload status"); empty input was rejected with "Invalid upload status", nothing changed | — |
| `bckrst`, `nwnvitem`, `fota`, `omadm`, `nua`, `factory_reset` tools | never run (rule 4) | — |

EFS paths that the stock software itself uses (found in `libmal_qct.so`; none read yet):

```
/nv/item_files/modem/mmode/lte_bandpref
/nv/item_files/modem/lte/rrc/csp/band_priority_list
/nv/item_files/modem/mmode/sxlte_timers
/nv/item_files/ims/qp_ims_voip_config
/nv/item_files/ims/qp_ims_sip_extended_0_config
/nv/item_files/ims/ims_sip_config
/nv/item_files/ims/qp_ims_sms_config
/nv/item_files/ims/qipcall_enable_hd_voice
/nv/item_files/ims/qipcall_codec_mode_set
/nv/item_files/ims/qipcall_codec_mode_set_amr_wb
/nv/item_files/ims/qp_ims_reg_extended_0_config
/nv/item_files/ims/qp_ims_presence_config
/nv/item_files/ims/qipcall_config_items
/nv/item_files/cne/1XDataServiceTransferTimer
/nv/item_files/cdma/1xcp/disable_so35_so36
```

Only the first two matter to us (they back up what `lte_band_get_enabled` shows and the B3 change). Reading
them is a read, but it goes through the same modem EFS interface as `efs_write`, hence the explicit OK.

## 16. Qualcomm diag (deepest level): findings and a proposal

Findings (read-only: `ps`, `lsof`, `/proc`, `/sys/kernel/debug/diag/*`, strings of the binaries):

- `/dev/diag` is open in modem2d, gpsd, smsd, modem_at_serverd, dsmd, powersaved, netmgrd, qti, ipacm and
  `diag_read`. These are ordinary diag clients (commands, events, NV). None of them is logging.
- `diag_read` (`/opt/nvtl/bin`, started by `diag_read.sh` only when the USB function list has `diag`) is not a
  log reader: it links fuel-gauge calls (`fuel_gauge_read_voltage`, `_current`, `_temperature`...), DSM calls and
  `nwqmi_nvtl_nv_item_read_cmd` / `_write_cmd`. It is the battery/NV helper. It also holds `/dev/smem_log`.
- Driver state: `Logging Mode: 0` (USB), `MD session mode: 0`, DCI clients 0, USB diag connected, modem
  feature mask `f7 ee`. So diag output goes to the USB diag function, and nobody reads it there.
- USB: the diag function is interface 3 (`ff/ff/ff`, "MI_03"); on this computer (Linux) no driver is bound
  to interfaces 2 and 3. Interface 2 (`ff/00/00`) is the serial port that port_bridge connects to `at_mdm0`.
- On the router: `/usr/bin/diag_mdlog`, `diag_socket_log`, `diag_klog`, `diag_uart_log`, `diag_dci_sample`,
  `diag_callback_sample`, `diagrebootapp`. No mask files (`*.cfg`), no `/sdcard` (the default output folder of
  diag_mdlog).

What it would take, three ways:

1. **From the computer over USB (recommended first).** Open-source SCAT (`scat -t qc -u -a <bus:dev> -i 3`)
   or QCSuper talk to the unclaimed diag interface directly, send the log masks themselves and write Wireshark
   pcaps (GSMTAP: RRC, NAS) plus parsed measurement lines. Nothing runs or is stored on the router. The router
   cost is the diag driver forwarding packets to USB (kernel threads); to be measured, I expect low single-digit
   CPU % for RRC + ML1 masks. Both tools have to be installed on the computer (pip, from GitHub): rule 5, ask.
2. **On the router with `diag_mdlog`.** Needs a mask file built on the computer, writes `.qmdl` to `/tmp` (RAM,
   ~110 MB free; ML1 + MAC logs can reach several MB per minute under load, so short sessions only, never to
   flash). It switches the global logging mode to memory-device, which cuts the USB diag path while it runs.
   More CPU on the router and more ways to go wrong. SCAT can parse the `.qmdl` offline afterwards
   (`scat -t qc -d file.qmdl`).
3. **DCI client for the panel later.** DCI lets one process subscribe to chosen log codes without changing the
   logging mode (the stock `diag_dci_sample` is an example). This is how the panel could show Tx power or
   timing advance live; it needs our own code on top of the diag ioctl API.

Log items needed for the wished values (codes as used by SCAT / MobileInsight; to be confirmed on this firmware):
LTE RRC OTA `0xB0C0` and serving cell info `0xB0C2` (RRC messages, SIBs), NAS EMM/ESM OTA `0xB0EC`/`0xB0ED`,
ML1 serving cell measurement `0xB193` and neighbours `0xB17F`/`0xB180` (RSRP/RSRQ/SINR per antenna),
PUSCH Tx report `0xB139` (Tx power, resource blocks, MCS), PUCCH Tx report `0xB13C`, PUSCH CSF `0xB14E`
(CQI, rank, PMI), PDSCH stat `0xB173` (MCS, transport blocks, CRC = BLER), MAC DL/UL transport block
`0xB063`/`0xB064` (power headroom, buffer status), MAC RACH attempt `0xB062` (timing advance from the random
access response). SCAT covers RRC/NAS and the ML1 measurements well; the PHY statistics (PDSCH/PUSCH/CSF)
are MobileInsight's strength.

Proposal, as a separate step with your OK: install SCAT on the computer after reading its source, capture
2 minutes over USB with RRC + NAS + ML1 serving/neighbour + PUSCH Tx masks only, watch router CPU over adb,
then look at the pcap and the measurement lines together. Diag log masks live in modem RAM only (no NV write); `diag_mdlog -c` clears them on exit, for SCAT this
has to be checked in its source, and a router reboot clears them in any case.

## 17. The most useful values the panel does not show yet

Ranked by how much they help with real problems (drops, 3G fallback, power cuts) per unit of cost.

| # | Value | Source | Where it fits |
|---|---|---|---|
| 1 | Data session history: every session start/end since the first use, 3G vs LTE | `dmdb.db` `DataSessions` (read a copy), later `ccm2.wan_connection` events | new **History** page (drops per day, session lengths); this is the original "drops every few minutes" problem as data |
| 2 | Battery voltage, current and temperature (and a fix: battery temp is in `temp_ambient`) | `bq27520-battery/voltage_now`, `current_now`, `temp_ambient` | Overview battery card; "time left on battery" estimate; Telegram power-cut alert |
| 3 | Power-on cause and power source | `dsm_cli get_state` `power_on_cause`, `power_state` | Overview / event log: "router restarted after power loss"; power-outage alerts |
| 4 | Per-client Wi-Fi signal and link rate | `wl -i wlanX rssi <MAC>`, `wl sta_info <MAC>` | Devices list: signal and speed column; screen widget "weakest client" |
| 5 | Network reject cause and last failure reason | `get_reject_cause_code`, `AT+CEER` | Cells page event log next to registration changes |
| 6 | Network time zone | `AT+CCLK?` (+08 = UTC+2), `get_network_time` | clock widget offset without the browser; correct event times |
| 7 | SoC temperature | `thermal_zone5..9` (tsens), `xo_therm` | System card; screen widget "Temperature" |
| 8 | Unread SMS count | msgbus `sms.list.unread` or `/srv/status` | Overview badge; screen widget; later Telegram forwarding |
| 9 | APN, gateway, operator DNS, /30 mask | `AT+CGCONTRDP`, `call_get_status`, `get_active_dns` | Internet card details |
| 10 | LTE band preference (B1..B43) and the applied list | `lte_band_get_enabled`, `get_diag_info` | read-only "Modem" section on the Cells page: shows B3 on, so the fix stays visible |
| 11 | Next LTE search interval when on 3G (300 s) | `bsr_get_timers` | Cells page hint on 3G: "the modem looks for LTE every 5 minutes" |
| 12 | Lifetime traffic | `dmdb_cli get_data_usage_statistics` | Overview traffic card ("since first use"); screen widget |
| 13 | Per-client data usage | `dmdb.db` `UsageLogs` | Devices list: data used per device |
| 14 | Blocked inbound connections from the internet | `iptables -L INPUT -v -n -x` counters on rmnet_data0 | System / security card: "LTE side closed, N attempts blocked" |
| 15 | Wi-Fi channel load and noise | `wl chanim_stats`, `wl noise`, `wl status` | Wi-Fi card: channel, band, how busy the air is |
| 16 | Flash wear | `/sys/class/ubi/ubi0/max_ec`, `bad_peb_count` | System card "storage health" (useful before autostart writes to flash) |
| 17 | SIM status and PIN retries | `sim_get_status` | About / SIM card with a warning when retries are low |
| 18 | Firmware, PRI and hardware versions, Wi-Fi chip | `get_info`, `wl ver` | About page (IMEI hidden by default) |
| 19 | Dormancy flag | msgbus `modem2.conn_dormancy` | Signal card "radio idle / active" (meaning to verify first) |
| 20 | Event-driven updates | message bus channels (`modem2.data_signal_change`, `ccm2.wan_connection`, `hostapd.ap.sta_connected`) | not a value: a cheaper source (20 ms read, or subscribe) to replace part of the 4 s polling |

## Appendix A. Every AT command from `AT+CLAC`

"Run" = which forms were sent. Results: `data` = OK with a value, `OK` = OK with nothing, `ERROR`, or
`no answer in 1 s`. Values are in `testdata/raw/at-read-forms.txt`.

| Command | Meaning | Run | Result |
|---|---|---|---|
| `&C` | DCD line mode | not run | setter |
| `&D` | DTR line mode | not run | setter |
| `&E` | unknown | not run | setter |
| `&F` | factory defaults | not run | restores factory defaults |
| `&S` | DSR line mode | not run | setter |
| `&V` | show current settings | read | `bare` data |
| `&W` | store settings | not run | writes the profile to NV |
| `E` | command echo | not run | setter (echo) |
| `I` | identification | read | `bare` data |
| `L` | speaker loudness | not run | setter |
| `M` | speaker mode | not run | setter |
| `Q` | quiet result codes | not run | setter |
| `V` | verbose result codes | not run | setter |
| `X` | result code set | not run | setter |
| `Z` | reset to profile | not run | resets the modem settings |
| `T` | tone dialling | not run | setter |
| `P` | pulse dialling | not run | setter |
| `\Q` | flow control | not run | setter |
| `\S` | show settings (Qualcomm) | not run | setter |
| `\V` | connect message format | not run | setter |
| `%V` | unknown | not run | unknown vendor command |
| `D` | dial | not run | dials |
| `A` | answer | not run | answers a call |
| `H` | hang up | not run | hangs up |
| `O` | back to data mode | not run | returns to data mode |
| `S0` | S-register | read | `?` data |
| `S2` | S-register | read | `?` data |
| `S3` | S-register | read | `?` data |
| `S4` | S-register | read | `?` data |
| `S5` | S-register | read | `?` data |
| `S6` | S-register | read | `?` data |
| `S7` | S-register | read | `?` data |
| `S8` | S-register | read | `?` data |
| `S9` | S-register | read | `?` data |
| `S10` | S-register | read | `?` data |
| `S11` | S-register | read | `?` data |
| `S30` | S-register | read | `?` data |
| `S103` | S-register | read | `?` data |
| `S104` | S-register | read | `?` data |
| `+FCLASS` | fax class | read | `?` ERROR; `=?` ERROR |
| `+ICF` | serial framing | read | `?` data; `=?` data |
| `+IFC` | serial flow control | read | `?` data; `=?` data |
| `+IPR` | serial speed | read | `?` data; `=?` data |
| `+GMI` | manufacturer | read | `bare` data; `?` ERROR; `=?` OK |
| `+GMM` | model | read | `bare` data; `?` ERROR; `=?` OK |
| `+GMR` | firmware revision | read | `bare` data; `?` ERROR; `=?` OK |
| `+GCAP` | capabilities | read | `bare` data; `?` ERROR; `=?` OK |
| `+GSN` | IMEI | read | `bare` data; `?` ERROR; `=?` OK |
| `+DR` | compression report | read | `?` data; `=?` data |
| `+DS` | data compression | read | `?` data; `=?` data |
| `+CMEE` | error verbosity | read | `?` data; `=?` data |
| `+WS46` | wireless network selection | read | `?` data; `=?` data |
| `+PACSP` | PLMN mode bit (CSP) | read | `?` ERROR; `=?` ERROR |
| `+CFUN` | radio/phone functionality | read | `?` data; `=?` data |
| `+CLCC` | current calls | read | `bare` data; `?` ERROR; `=?` OK |
| `+CBST` | bearer service type | read | `?` data; `=?` data |
| `+CRLP` | radio link protocol | read | `?` data; `=?` data |
| `+CV120` | V.120 settings | read | `?` data; `=?` data |
| `+CHSN` | HSCSD settings | read | `?` data; `=?` data |
| `+CSSN` | supplementary service notifications | read | `?` data; `=?` data |
| `+CREG` | CS registration | read | `?` data; `=?` data |
| `+CGREG` | GPRS registration | read | `?` data; `=?` data |
| `+CEREG` | EPS (LTE) registration, TAC and cell with n=2 | read | `?` data; `=?` data |
| `+CSCS` | character set | read | `?` data; `=?` data |
| `+CSTA` | address type | read | `?` data; `=?` data |
| `+CR` | service reporting | read | `?` data; `=?` data |
| `+CEER` | last call/attach failure reason | read | `bare` data; `?` ERROR; `=?` OK |
| `+CRC` | cellular result codes | read | `?` data; `=?` data |
| `+CGDCONT` | PDP contexts (APNs) | read | `?` data; `=?` data |
| `+CGDSCONT` | secondary PDP contexts | read | `?` data; `=?` data |
| `+CGTFT` | traffic flow templates | read | `?` data; `=?` data |
| `+CGEQREQ` | 3G QoS requested | read | `?` data; `=?` data |
| `+CGEQMIN` | 3G QoS minimum | read | `?` data; `=?` data |
| `+CGEQOS` | EPS QoS | read | `?` data; `=?` data |
| `+CGCONTRDP` | active context: APN, IP, DNS | read | `bare` data; `?` ERROR; `=?` data |
| `+CGSCONTRDP` | secondary context details | read | `bare` OK; `?` ERROR; `=?` OK |
| `+CGTFTRDP` | active TFTs | read | `bare` OK; `?` ERROR; `=?` OK |
| `+CGEQOSRDP` | active EPS QoS (QCI, bit rates) | read | `bare` OK; `?` ERROR; `=?` OK |
| `+CGQREQ` | GPRS QoS requested | read | `?` data; `=?` data |
| `+CGQMIN` | GPRS QoS minimum | read | `?` data; `=?` data |
| `+CGEREP` | packet event reporting | read | `?` data; `=?` data |
| `+CGPADDR` | context IP addresses | read | `bare` data; `?` ERROR; `=?` data |
| `+CGDATA` | enter data state | not run | enters data mode |
| `+CGCLASS` | GPRS class | read | `?` data; `=?` data |
| `+CGPIAF` | IPv6 address format | read | `?` data; `=?` data |
| `+CGSMS` | SMS bearer | read | `?` data; `=?` data |
| `+CSMS` | SMS service | read | `?` data; `=?` data |
| `+CMGF` | SMS format | read | `?` data; `=?` data |
| `+CSAS` | save SMS settings | not run | saves SMS settings |
| `+CRES` | restore SMS settings | not run | restores SMS settings |
| `+CSCA` | SMS centre number | read | `?` data; `=?` OK |
| `+CSMP` | SMS text parameters | read | `?` data; `=?` OK |
| `+CSDH` | show SMS text header | read | `?` data; `=?` data |
| `+CSCB` | cell broadcast channels | read | `?` data; `=?` data |
| `+ES` | synchronous access mode | read | `?` data; `=?` data |
| `+ESA` | synchronous access settings | read | `?` data; `=?` data |
| `+CMOD` | call mode | read | `?` data; `=?` data |
| `+CEMODE` | UE mode of operation (CS/PS) | read | `?` data; `=?` data |
| `+CVHU` | voice hang-up control | read | `?` data; `=?` data |
| `+QCGEQMIN` | Qualcomm 3G QoS minimum | read | `?` data; `=?` data |
| `+QCGEQOS` | Qualcomm EPS QoS | read | `?` data; `=?` data |
| `+QCGEQREQ` | Qualcomm 3G QoS requested | read | `?` data; `=?` data |
| `+QCGQMIN` | Qualcomm GPRS QoS minimum | read | `?` data; `=?` data |
| `+QCGQREQ` | Qualcomm GPRS QoS requested | read | `?` data; `=?` data |
| `+QCGTFT` | Qualcomm TFT | read | `?` data; `=?` data |
| `+CEMBMSCFG` | eMBMS config | read | `?` data; `=?` data |
| `+CEMBMSSRV` | eMBMS service | read | `?` ERROR; `=?` ERROR |
| `+CEMBMSDATA` | eMBMS data | read | `?` ERROR; `=?` ERROR |
| `+CEMBMSCNT` | eMBMS count | read | `?` ERROR; `=?` ERROR |
| `+CEMBMSSAI` | eMBMS SAI list | read | `?` ERROR; `=?` ERROR |
| `+CSQ` | signal quality (3GPP) | read | `bare` data; `?` data; `=?` data |
| `+CBC` | battery charge (modem view) | read | `bare` data; `?` ERROR; `=?` data |
| `+CPAS` | activity status | read | `bare` data; `?` ERROR; `=?` data |
| `+CPIN` | SIM PIN status | read | `?` data; `=?` OK |
| `+CMEC` | mobile equipment control | read | `?` data; `=?` data |
| `+CIND` | indicators (battery, signal 0-5, service, roam...) | read | `?` data; `=?` data |
| `+CMER` | indicator event reporting | read | `?` data; `=?` data |
| `+CGATT` | packet attach state | read | `?` data; `=?` data |
| `+CGACT` | context activation state | read | `?` data; `=?` data |
| `+CGCMOD` | modify context | read | `?` ERROR; `=?` data |
| `+CPBS` | phonebook storage | read | `?` data; `=?` data |
| `+CPBR` | read phonebook | read | `?` ERROR; `=?` data |
| `+CPBF` | find in phonebook | read | `?` ERROR; `=?` data |
| `+CPBW` | write phonebook | not run | writes the phonebook |
| `+CPMS` | SMS storage and counts | read | `?` data; `=?` data |
| `+CNMI` | new SMS indications | read | `?` data; `=?` data |
| `+CMGL` | list SMS | not run | lists SMS and marks them read |
| `+CMGR` | read SMS | not run | reads an SMS and marks it read |
| `+CMGS` | send SMS | not run | sends SMS |
| `+CMSS` | send stored SMS | not run | sends a stored SMS |
| `+CMGW` | store SMS | not run | writes an SMS to storage |
| `+CMGD` | delete SMS | not run | deletes SMS |
| `+CMGC` | SMS command | not run | sends an SMS command |
| `+CNMA` | SMS ack | not run | acknowledges an SMS |
| `+CMMS` | more messages to send | not run | SMS link control |
| `+CHUP` | hang up | not run | hangs up |
| `+CCFC` | call forwarding | read | `?` ERROR; `=?` data |
| `+CCUG` | closed user group | read | `?` data; `=?` OK |
| `+COPS` | operator selection (=? scans) | read | `?` data |
| `+CLCK` | facility lock | not run | SIM/facility locks |
| `+CPWD` | change password | not run | changes SIM passwords |
| `+CUSD` | USSD | read | `?` data; `=?` data |
| `+CAOC` | advice of charge | read | `?` data; `=?` data |
| `+CACM` | accumulated call meter | read | `?` data; `=?` OK |
| `+CAMM` | call meter maximum | read | `?` data; `=?` OK |
| `+CPUC` | price per unit | read | `?` data; `=?` OK |
| `+CCWA` | call waiting | read | `?` data; `=?` data |
| `+CHLD` | call hold | read | `?` ERROR; `=?` data |
| `+CIMI` | IMSI | read | `bare` data; `?` ERROR; `=?` OK |
| `+CGMI` | manufacturer | read | `bare` no answer in 1 s; `?` ERROR; `=?` OK |
| `+CGMM` | model | read | `bare` data; `?` ERROR; `=?` OK |
| `+CGMR` | revision | read | `bare` no answer in 1 s; `?` ERROR; `=?` OK |
| `+CGSN` | IMEI | read | `bare` no answer in 1 s; `?` ERROR; `=?` OK |
| `+CNUM` | own number | read | `bare` ERROR; `?` ERROR; `=?` OK |
| `+CSIM` | raw SIM access | not run | raw SIM access (can write) |
| `+CRSM` | restricted SIM access | not run | raw SIM access (can write) |
| `+CCLK` | clock (network time, quarter-hour zone) | read | `?` data; `=?` OK |
| `+COPN` | operator name table | read | `bare` data; `?` ERROR; `=?` OK |
| `+CPOL` | preferred operator list (SIM) | read | `?` OK; `=?` data |
| `+CPLS` | PLMN list selection | read | `?` data; `=?` data |
| `+CTZR` | time zone reporting | read | `?` data; `=?` data |
| `+CTZU` | automatic time zone update | read | `?` data; `=?` data |
| `+CLAC` | list commands | not run | already run (this list) |
| `+CLIP` | caller ID presentation | read | `?` no answer in 1 s; `=?` ERROR |
| `+COLP` | connected line presentation | read | `?` data; `=?` data |
| `+CDIP` | called line presentation | read | `?` no answer in 1 s; `=?` ERROR |
| `+CTFR` | call deflection | not run | call deflection |
| `+CLIR` | caller ID restriction | read | `?` no answer in 1 s; `=?` ERROR |
| `+VTS` | DTMF | not run | sends DTMF |
| `+ICCID` | SIM ICCID (answers as $NWICCID) | read | `bare` ERROR; `?` data; `=?` OK |
| `+CUAD` | UICC applications | read | `bare` data; `?` ERROR; `=?` ERROR |
| `+CEAP` | EAP auth | not run | EAP authentication on the SIM |
| `+CERP` | EAP response | not run | EAP response on the SIM |
| `+CSTF` | time format | read | `?` ERROR; `=?` data |
| `+CSDF` | date format | read | `?` ERROR; `=?` data |
| `+CVMOD` | voice mode | read | `?` data; `=?` data |
| `+CEN` | emergency numbers | read | `?` data; `=?` data |
| `+CNMPSD` | no more PS data | not run | tells the network there is no more PS data (can release the connection) |
| `+CMCCS` | mobile country code setting | read | `?` data; `=?` data |
| `+CCMMD` | unknown | read | `?` OK; `=?` OK |
| `$QCSIMSTAT` | SIM state | read | `?` data; `=?` data |
| `$QCPBMPREF` | phonebook preference | read | `?` data; `=?` data |
| `$CREG` | registration (Qualcomm) | read | `?` data; `=?` data |
| `$CCLK` | clock (Qualcomm) | read | `?` data; `=?` OK |
| `$QCCNMI` | SMS indications (Qualcomm) | read | `?` data; `=?` data |
| `*CNTI` | technology in use | read | `?` data; `=?` data |
| `$QCCLR` | clear error log | not run | clears the modem error log |
| `$QCDMR` | DM port speed | read | `?` data; `=?` data |
| `$QCDNSP` | primary DNS for dial-up | read | `?` data; `=?` data |
| `$QCDNSS` | secondary DNS for dial-up | read | `?` data; `=?` data |
| `$QCTER` | TE-DCE rate | read | `?` data; `=?` data |
| `$QCSIMAPP` | SIM application | read | `?` data; `=?` data |
| `$QCPINSTAT` | PIN/PUK status | read | `bare` ERROR; `?` data; `=?` OK |
| `$QCPDPP` | PDP authentication | read | `?` data; `=?` data |
| `$QCPDPLT` | PDP linger timer | read | `?` data; `=?` data |
| `$QCPWRDN` | power down | not run | powers the modem down |
| `$QCDGEN` | data generator | not run | generates data traffic |
| `$QCPDPCFGE` | PDP config (Qualcomm) | read | `?` data; `=?` data |
| `$QCANTE` | antenna info (?) | read | `?` ERROR; `=?` data |
| `$QCRPW` | received power (?) | read | `?` ERROR; `=?` data |
| `$QCSQ` | signal (RSCP, Ec/Io, SINR, ?, RSSI) | read | `bare` data; `?` ERROR; `=?` data |
| `$CSQ` | signal (vendor) | read | `bare` data; `?` data; `=?` data |
| `$QCSYSMODE` | system mode text | read | `?` data; `=?` ERROR |
| `$QCCTM` | TTY mode | read | `?` data; `=?` data |
| `$QCBANDPREF` | WCDMA band preference | read | `?` data; `=?` data |
| `^SYSINFO` | service, domain, roam, system mode, SIM | read | `bare` data; `?` ERROR; `=?` ERROR |
| `^SYSCONFIG` | mode and acquisition order | read | `?` data; `=?` ERROR |
| `^CARDMODE` | SIM card type | read | `bare` data; `?` ERROR; `=?` ERROR |
| `^DSCI` | call status indication | read | `?` data; `=?` data |
| `$QCVOLT` | voltage (modem view) | read | `bare` data; `?` ERROR; `=?` ERROR |
| `$QCHWREV` | hardware revision | read | `bare` data; `?` ERROR; `=?` ERROR |
| `$QCBOOTVER` | boot version | read | `bare` ERROR; `?` ERROR; `=?` ERROR |
| `$QCDEFPROF` | default profile | read | `?` data; `=?` data |
| `$QCMRUE` | most recently used bands/channels | read | `?` data; `=?` data |
| `$QCMRUC` | clear MRU | not run | clears the most-recently-used network list |
| `$QCAPNE` | APN table (Qualcomm) | read | `?` data; `=?` data |
| `$QCPDPIMSCFGE` | IMS PDP config | read | `?` data; `=?` data |
| `$QCCLAC` | list commands (Qualcomm) | read | `bare` data; `?` data; `=?` OK |
| `^SPN` | service provider name | read | `?` ERROR; `=?` ERROR |
| `$QCRMCALL` | rmnet call | not run | starts/stops an rmnet data call |
| `$QCDRX` | DRX cycle | read | `?` data; `=?` data |
| `$QCRSRP` | LTE cells: PCI, EARFCN, RSRP | read | `?` data; `=?` ERROR |
| `$QCRSRQ` | LTE cells: PCI, EARFCN, RSRQ | read | `?` data; `=?` ERROR |
| `$QCACQDBC` | clear acquisition database | not run | clears the network acquisition database |
| `$QCATMOD` | ATCoP mode | read | `?` data; `=?` data |
| `$QCCOPS` | operator (Qualcomm, =? scans) | read | `?` data |
| `^MODE` | mode reporting | read | `?` data; `=?` data |
| `$QCSIMT` | SIM type | read | `?` data; `=?` ERROR |
| `$QCNSP` | network selection preference | read | `?` data; `=?` data |
| `$QCRCIND` | ring/call indication | read | `?` data; `=?` data |
| `$QCCSGCOPS` | CSG operator (=? scans) | read | `?` data |
| `$QCPDPCFGEXT` | PDP config ext | read | `?` data; `=?` data |
| `$QCPRFCRT` | create profile | not run | creates a data profile |
| `$QCPRFMOD` | profile settings | read | `?` data; `=?` data |
| `$QCRSCP` | WCDMA cells RSCP | read | `?` data; `=?` ERROR |
| `$QCECNO` | WCDMA cells Ec/No | read | `?` data; `=?` ERROR |
| `$QCPCOLIST` | PCO list | read | `?` OK; `=?` OK |
| `$QCMSISDN` | MSISDN (own number) | read | `?` OK; `=?` OK |
| `$QCPCOMSISDN` | PCO MSISDN config | read | `?` data; `=?` data |
| `$QCCALLATTR` | call attributes | read | `?` data; `=?` data |

## Appendix B. Every vendor CLI subcommand

From the help output of each tool (`probe-modem.txt`, and `modem2_cli help` for the full modem2 list).
`hostapd_cli` and `wl` are covered in section 9; `mifi_debug_cli` only lists log levels without arguments.

| Tool | Subcommand | Run | Note |
|---|---|---|---|
| `ans_cli` | `print_error_codes` | no | meta (no device data) |
| `ans_cli` | `is_running` | no | meta (no device data) |
| `ans_cli` | `get_notification_settings` | yes | see vendor-getters.txt |
| `ans_cli` | `set_notification_settings` | no | setter / writes (rule 4) |
| `ans_cli` | `get_stealth_mode_enabled` | yes | see vendor-getters.txt |
| `ans_cli` | `set_stealth_mode_enabled` | no | setter / writes (rule 4) |
| `ans_cli` | `get_audio_alerts_enabled` | yes | see vendor-getters.txt |
| `ans_cli` | `set_audio_alerts_enabled` | no | setter / writes (rule 4) |
| `ans_cli` | `get_current_notification_list` | yes | see vendor-getters.txt |
| `ans_cli` | `acknowledge_notification` | no | action / changes state |
| `ans_cli` | `trigger_notification` | no | action / changes state |
| `bckrst_cli` | (all) | never | rule 4: never run |
| `buzzer_cli` | `print_error_codes` | no | meta (no device data) |
| `buzzer_cli` | `on` | no | action / changes state |
| `buzzer_cli` | `off` | no | action / changes state |
| `cc_cli` | `print_error_codes` | no | meta (no device data) |
| `cc_cli` | `is_running` | no | meta (no device data) |
| `cc_cli` | `carrier_count` | yes | see vendor-getters.txt |
| `cc_cli` | `carrier_enable` | yes | see vendor-getters.txt |
| `cc_cli` | `carrier_data` | yes | see vendor-getters.txt |
| `cc_cli` | `carrier_label_count` | yes | see vendor-getters.txt |
| `cc_cli` | `carrier_label_list` | yes | see vendor-getters.txt |
| `cc_cli` | `current_carrier_id` | yes | see vendor-getters.txt |
| `cc_cli` | `cc_folder` | yes | see vendor-getters.txt |
| `ccm2_cli` | `rc_to_str` | no | meta |
| `ccm2_cli` | `print_error_codes` | no | meta (no device data) |
| `ccm2_cli` | `is_running` | no | meta (no device data) |
| `ccm2_cli` | `get_ipv6_enabled` | yes | see vendor-getters.txt |
| `ccm2_cli` | `set_ipv6_enabled` | no | setter / writes (rule 4) |
| `ccm2_cli` | `get_cell_data_enabled` | yes | see vendor-getters.txt |
| `ccm2_cli` | `set_cell_data_enabled` | no | setter / writes (rule 4) |
| `ccm2_cli` | `get_wifi_enabled` | yes | see vendor-getters.txt |
| `ccm2_cli` | `set_wifi_enabled` | no | setter / writes (rule 4) |
| `ccm2_cli` | `get_wifi_ap_enabled` | yes | see vendor-getters.txt |
| `ccm2_cli` | `set_wifi_ap_enabled` | no | setter / writes (rule 4) |
| `ccm2_cli` | `get_wifi_stn_enabled` | yes | see vendor-getters.txt |
| `ccm2_cli` | `set_wifi_stn_enabled` | no | setter / writes (rule 4) |
| `ccm2_cli` | `get_air_mode_enabled` | yes | see vendor-getters.txt |
| `ccm2_cli` | `set_air_mode_enabled` | no | setter / writes (rule 4) |
| `ccm2_cli` | `get_wifi_while_usbteth` | yes | see vendor-getters.txt |
| `ccm2_cli` | `set_wifi_while_usbteth` | no | setter / writes (rule 4) |
| `ccm2_cli` | `set_wifi_while_usbteth_temp` | no | setter / writes (rule 4) |
| `ccm2_cli` | `get_static_ip` | yes | see vendor-getters.txt |
| `ccm2_cli` | `set_static_ip` | no | setter / writes (rule 4) |
| `ccm2_cli` | `get_ntp_url` | yes | see vendor-getters.txt |
| `ccm2_cli` | `set_ntp_url` | no | setter / writes (rule 4) |
| `ccm2_cli` | `get_curr_wan` | yes | see vendor-getters.txt |
| `ccm2_cli` | `get_curr_wan_stats` | yes | see vendor-getters.txt |
| `ccm2_cli` | `get_conn_state` | yes | see vendor-getters.txt |
| `ccm2_cli` | `get_wan_prio` | yes | see vendor-getters.txt |
| `ccm2_cli` | `set_wan_prio` | no | setter / writes (rule 4) |
| `ccm2_cli` | `wan_connect` | no | action / changes state |
| `ccm2_cli` | `wan_disconnect` | no | action / changes state |
| `ccm2_cli` | `get_wwan_ac` | yes | see vendor-getters.txt |
| `ccm2_cli` | `set_wwan_ac` | no | setter / writes (rule 4) |
| `ccm2_cli` | `get_wwan_ac_state` | yes | see vendor-getters.txt |
| `ccm2_cli` | `set_wwan_ac_client_count` | no | setter / writes (rule 4) |
| `ccm2_cli` | `wwan_connect` | no | action / changes state |
| `ccm2_cli` | `wwan_disconnect` | no | action / changes state |
| `ccm2_cli` | `apps_call_start` | no | action / changes state |
| `ccm2_cli` | `apps_call_stop` | no | action / changes state |
| `ccm2_cli` | `apps_call_state` | no | rule 4 lists ccm2 apps_call_* as needing your OK (looks read-only) |
| `ccm2_cli` | `dm_call_start` | no | action / changes state |
| `ccm2_cli` | `dm_call_stop` | no | action / changes state |
| `ccm2_cli` | `dm_call_state` | no | rule 4 lists ccm2 dm_call_* as needing your OK (looks read-only) |
| `ccm2_cli` | `accept_pco_event` | no | simulated event / private API (rule 4) |
| `ccm2_cli` | `get_airplane_mode_disable_status` | yes | see vendor-getters.txt |
| `ccm2_cli` | `get_call_throttle_state` | yes | see vendor-getters.txt |
| `ccm2_cli` | `update_roam_state` | no | simulated event / private API (rule 4) |
| `ccm2_cli` | `update_usb_iface` | no | simulated event / private API (rule 4) |
| `ccm2_cli` | `set_network_time` | no | setter / writes (rule 4) |
| `ccm2_cli` | `send_wan_stats_evt` | no | action / changes state |
| `ccm2_cli` | `update_pco_event` | no | simulated event / private API (rule 4) |
| `cdra_cli` | `is_running` | no | meta (no device data) |
| `cdra_cli` | `upload_data` | no | action / changes state |
| `cdra_cli` | `server_upload_status` | yes | see vendor-getters.txt |
| `cumclient_cli` | `print_error_codes` | no | meta (no device data) |
| `cumclient_cli` | `is_running` | no | meta (no device data) |
| `cumclient_cli` | `is_connect_to_server` | yes | see vendor-getters.txt |
| `cumclient_cli` | `get_s1_version` | yes | see vendor-getters.txt |
| `cumclient_cli` | `get_s1_name` | yes | see vendor-getters.txt |
| `cumclient_cli` | `set_s1_name` | no | setter / writes (rule 4) |
| `cumclient_cli` | `get_s1_packet` | yes | see vendor-getters.txt |
| `cumclient_cli` | `set_s1_packet` | no | setter / writes (rule 4) |
| `cumclient_cli` | `get_s1_traffic` | yes | see vendor-getters.txt |
| `cumclient_cli` | `get_s1_last_transmission` | yes | see vendor-getters.txt |
| `cumclient_cli` | `get_s1_gps_include` | yes | see vendor-getters.txt |
| `cumclient_cli` | `set_s1_gps_include` | no | setter / writes (rule 4) |
| `cumclient_cli` | `get_s1_data_enable` | yes | see vendor-getters.txt |
| `cumclient_cli` | `set_s1_data_enable` | no | setter / writes (rule 4) |
| `cumclient_cli` | `get_s1_port` | yes | see vendor-getters.txt |
| `cumclient_cli` | `get_s1_conn` | yes | see vendor-getters.txt |
| `cumclient_cli` | `get_s1_url` | yes | see vendor-getters.txt |
| `cumclient_cli` | `get_s1_data_int` | yes | see vendor-getters.txt |
| `cumclient_cli` | `set_s1_data_int` | no | setter / writes (rule 4) |
| `devui_cli` | `print_error_codes` | no | meta (no device data) |
| `devui_cli` | `is_running` | no | meta (no device data) |
| `devui_cli` | `get_guest_wifi_info_display_settings` | yes | see vendor-getters.txt |
| `devui_cli` | `set_guest_wifi_info_display_settings` | no | setter / writes (rule 4) |
| `devui_cli` | `get_primary_wifi_info_display_settings` | yes | see vendor-getters.txt |
| `devui_cli` | `set_primary_wifi_info_display_settings` | no | setter / writes (rule 4) |
| `devui_cli` | `get_display_setting_enabled` | yes | see vendor-getters.txt |
| `devui_cli` | `set_display_setting_enabled` | no | setter / writes (rule 4) |
| `devui_cli` | `get_screen_timeout_settings` | yes | see vendor-getters.txt |
| `devui_cli` | `set_screen_timeout_settings` | no | setter / writes (rule 4) |
| `devui_cli` | `get_quickstart_enabled` | yes | see vendor-getters.txt |
| `devui_cli` | `set_quickstart_enabled` | no | setter / writes (rule 4) |
| `devui_cli` | `get_usb_connection_prompt_enabled` | yes | see vendor-getters.txt |
| `devui_cli` | `set_usb_connection_prompt_enabled` | no | setter / writes (rule 4) |
| `devui_cli` | `get_features` | yes | see vendor-getters.txt |
| `devui_cli` | `get_configurations` | yes | see vendor-getters.txt |
| `devui_cli` | `get_apps_menu_info` | yes | see vendor-getters.txt |
| `devui_cli` | `get_file_sharing_display_setting_enabled` | yes | see vendor-getters.txt |
| `devui_cli` | `set_file_sharing_display_setting_enabled` | no | setter / writes (rule 4) |
| `devui_cli` | `get_wifi_display_setting_enabled` | yes | see vendor-getters.txt |
| `devui_cli` | `set_wifi_display_setting_enabled` | no | setter / writes (rule 4) |
| `devui_cli` | `get_admin_password_enabled` | yes | see vendor-getters.txt |
| `devui_cli` | `set_admin_password_enabled` | no | setter / writes (rule 4) |
| `devui_cli` | `get_notification_display_wakeup_setting_enabled` | yes | see vendor-getters.txt |
| `devui_cli` | `set_notification_display_wakeup_setting_enabled` | no | setter / writes (rule 4) |
| `devui_cli` | `get_info_panels_list` | yes | see vendor-getters.txt |
| `dmdb_cli` | `update_device_list` | no | simulated event / private API (rule 4) |
| `dmdb_cli` | `update_wan_session_states` | no | simulated event / private API (rule 4) |
| `dmdb_cli` | `new_data_session` | no | action / changes state |
| `dmdb_cli` | `update_data_session` | no | simulated event / private API (rule 4) |
| `dmdb_cli` | `update_site_access` | no | simulated event / private API (rule 4) |
| `dmdb_cli` | `configure_site_access_monitor` | no | action / changes state |
| `dmdb_cli` | `configure_mac_filter` | no | action / changes state |
| `dmdb_cli` | `configure_white_list` | no | action / changes state |
| `dmdb_cli` | `configure_black_list` | no | action / changes state |
| `dmdb_cli` | `update_sim_info` | no | simulated event / private API (rule 4) |
| `dmdb_cli` | `rc_to_str` | no | meta |
| `dmdb_cli` | `print_error_codes` | no | meta (no device data) |
| `dmdb_cli` | `is_running` | no | meta (no device data) |
| `dmdb_cli` | `get_configuration` | yes | see vendor-getters.txt |
| `dmdb_cli` | `set_device` | no | setter / writes (rule 4) |
| `dmdb_cli` | `get_device` | no | needs a MAC; same data as the device lists |
| `dmdb_cli` | `get_known_device_list` | yes | see vendor-getters.txt |
| `dmdb_cli` | `get_connected_device_list` | yes | see vendor-getters.txt |
| `dmdb_cli` | `get_device_settings_list` | yes | see vendor-getters.txt |
| `dmdb_cli` | `set_device_settings_list` | no | setter / writes (rule 4) |
| `dmdb_cli` | `delete_device` | no | action / changes state |
| `dmdb_cli` | `get_list_by_name` | no | needs a name; same data as the lists |
| `dmdb_cli` | `get_whitelist` | yes | see vendor-getters.txt |
| `dmdb_cli` | `search_whitelist` | no | needs a MAC; same data as the get_* lists |
| `dmdb_cli` | `get_blacklist` | yes | see vendor-getters.txt |
| `dmdb_cli` | `search_blacklist` | no | needs a MAC; same data as the get_* lists |
| `dmdb_cli` | `get_internet_blocked_list` | yes | see vendor-getters.txt |
| `dmdb_cli` | `search_internet_blocked_list` | no | needs a MAC; same data as the get_* lists |
| `dmdb_cli` | `get_reserved_ip_list` | yes | see vendor-getters.txt |
| `dmdb_cli` | `search_reserved_ip_list` | no | needs a MAC; same data as the get_* lists |
| `dmdb_cli` | `get_mac_filter_enabled` | yes | see vendor-getters.txt |
| `dmdb_cli` | `set_mac_filter_enabled` | no | setter / writes (rule 4) |
| `dmdb_cli` | `get_mac_filter_type` | yes | see vendor-getters.txt |
| `dmdb_cli` | `set_mac_filter_type` | no | setter / writes (rule 4) |
| `dmdb_cli` | `get_data_usage_statistics` | yes | see vendor-getters-prompted.txt |
| `dmdb_cli` | `get_usage_reset_date` | yes | see vendor-getters.txt |
| `dmdb_cli` | `delete_usage_prior_to_date` | no | action / changes state |
| `dmdb_cli` | `get_session_log` | yes | see vendor-getters.txt |
| `dmdb_cli` | `set_usage_data` | no | setter / writes (rule 4) |
| `dmdb_cli` | `get_usage_log` | yes | see vendor-getters.txt |
| `dmdb_cli` | `get_traffic_status` | yes | see vendor-getters.txt |
| `dmdb_cli` | `get_client_upload_records` | yes | see vendor-getters.txt |
| `dmdb_cli` | `set_client_upload_state` | no | setter / writes (rule 4) |
| `dmdb_cli` | `get_site_access_monitor_enabled` | yes | see vendor-getters.txt |
| `dmdb_cli` | `set_site_access_monitor_enabled` | no | setter / writes (rule 4) |
| `dmdb_cli` | `get_site_access_statistics` | yes | see vendor-getters.txt |
| `dmdb_cli` | `delete_sites_prior_to_date` | no | action / changes state |
| `dsm_cli` | `print_error_codes` | no | meta (no device data) |
| `dsm_cli` | `is_running` | no | meta (no device data) |
| `dsm_cli` | `get_state` | yes | see vendor-getters.txt |
| `dsm_cli` | `set_persistent_mode` | no | setter / writes (rule 4) |
| `dsm_cli` | `shutdown_device` | no | action / changes state |
| `dsm_cli` | `reset_device` | no | setter / writes (rule 4) |
| `dsm_cli` | `devui_safe_shutdown_ack` | no | action / changes state |
| `dsm_cli` | `lpm_safe_exit_ack` | no | action / changes state |
| `dsm_cli` | `set_display_state` | no | setter / writes (rule 4) |
| `dsm_cli` | `get_settings` | yes | see vendor-getters.txt |
| `dsm_cli` | `set_settings` | no | setter / writes (rule 4) |
| `dsm_cli` | `send_battery_id` | no | action / changes state |
| `dsm_cli` | `send_battery_temperature_state` | no | action / changes state |
| `dsm_cli` | `send_battery_temperature_value` | no | action / changes state |
| `dsm_cli` | `send_battery_voltage_state` | no | action / changes state |
| `dsm_cli` | `send_battery_capacity` | no | action / changes state |
| `dsm_cli` | `settings_changed` | no | setter / writes (rule 4) |
| `dsm_cli` | `send_modem_safe_shutdown_ack` | no | action / changes state |
| `dsm_cli` | `send_modem_state_changed` | no | action / changes state |
| `dsm_cli` | `set_suspend_wakeup_time` | no | setter / writes (rule 4) |
| `dsm_cli` | `set_usb_device_type` | no | setter / writes (rule 4) |
| `dsm_cli` | `set_usb_typec_pwr_role_pref` | no | setter / writes (rule 4) |
| `dsm_cli` | `get_ui_controls_shutdown_enabled` | yes | see vendor-getters.txt |
| `dsm_cli` | `get_default_usb_mode` | yes | see vendor-getters.txt |
| `dsm_cli` | `set_default_usb_mode` | no | setter / writes (rule 4) |
| `dsm_cli` | `get_usb_mac_address` | yes | see vendor-getters.txt |
| `dsm_cli` | `get_wifi_mac_address` | yes | see vendor-getters-prompted.txt |
| `dsm_cli` | `get_persistent_rl` | yes | see vendor-getters.txt |
| `dsm_cli` | `set_persistent_rl` | no | setter / writes (rule 4) |
| `dua_cli` | `print_error_codes` | no | meta (no device data) |
| `dua_cli` | `is_running` | no | meta (no device data) |
| `dua_cli` | `trigger_data_usage_sync` | no | action / changes state |
| `dua_cli` | `get_data_usage_info` | yes | see vendor-getters.txt |
| `dua_cli` | `get_data_usage_settings_info` | yes | see vendor-getters.txt |
| `dua_cli` | `set_data_usage_settings_info` | no | setter / writes (rule 4) |
| `emd_cli` | `is_running` | no | meta (no device data) |
| `emd_cli` | `log` | no | writes internal state to the system log |
| `emd_cli` | `get_iface_type` | yes | see vendor-getters.txt |
| `emd_cli` | `set_iface_type` | no | setter / writes (rule 4) |
| `emd_cli` | `get_iface_status` | yes | see vendor-getters.txt |
| `emd_cli` | `set_iface_status` | no | setter / writes (rule 4) |
| `emd_cli` | `get_eth_static` | yes | see vendor-getters.txt |
| `emd_cli` | `set_eth_static` | no | setter / writes (rule 4) |
| `emd_cli` | `connect_wan` | no | action / changes state |
| `factory_reset_cli` | (all) | never | rule 4: never run |
| `file_sharing_cli` | `print_error_codes` | no | meta (no device data) |
| `file_sharing_cli` | `is_running` | no | meta (no device data) |
| `file_sharing_cli` | `settings_get` | yes | see vendor-getters.txt |
| `file_sharing_cli` | `settings_set` | no | setter / writes (rule 4) |
| `file_sharing_cli` | `settings_list_get` | yes | see vendor-getters.txt |
| `file_sharing_cli` | `settings_list_set` | no | setter / writes (rule 4) |
| `file_sharing_cli` | `start_stop_share` | no | action / changes state |
| `file_sharing_cli` | `get_file_sharing_state` | yes | see vendor-getters.txt |
| `file_sharing_cli` | `get_file_system_type` | yes | see vendor-getters.txt |
| `file_sharing_cli` | `get_smb_host_name` | yes | see vendor-getters.txt |
| `file_sharing_cli` | `mount_umount_partition` | no | action / changes state |
| `file_sharing_cli` | `change_usb_state` | no | action / changes state |
| `file_sharing_cli` | `set_file_system_type` | no | setter / writes (rule 4) |
| `file_sharing_cli` | `service_monitor_add` | no | action / changes state |
| `file_sharing_cli` | `service_monitor_list` | yes | see vendor-getters.txt |
| `fota_cli` | (all) | never | rule 4: never run |
| `gps_cli` | `rc_to_str` | no | meta |
| `gps_cli` | `print_error_codes` | no | meta (no device data) |
| `gps_cli` | `is_running` | no | meta (no device data) |
| `gps_cli` | `update_wan_connection` | no | simulated event / private API (rule 4) |
| `gps_cli` | `agps_mode_set` | no | setter / writes (rule 4) |
| `gps_cli` | `force_xtra` | no | action / changes state |
| `gps_cli` | `get_last_fix` | yes | see vendor-getters.txt |
| `gps_cli` | `gps_status` | yes | see vendor-getters.txt |
| `gps_cli` | `gps_start` | no | action / changes state |
| `gps_cli` | `gps_stop` | no | action / changes state |
| `gps_cli` | `get_active` | yes | see vendor-getters.txt |
| `gps_cli` | `set_active` | no | setter / writes (rule 4) |
| `gps_cli` | `get_nmea_tcp` | yes | see vendor-getters.txt |
| `gps_cli` | `set_nmea_tcp` | no | setter / writes (rule 4) |
| `gps_cli` | `get_privacy` | yes | see vendor-getters.txt |
| `gps_cli` | `set_privacy` | no | setter / writes (rule 4) |
| `gps_cli` | `get_mode` | yes | see vendor-getters.txt |
| `gps_cli` | `enable_powersave_mode` | no | action / changes state |
| `led_cli` | `print_error_codes` | no | meta (no device data) |
| `led_cli` | `is_running` | no | meta (no device data) |
| `led_cli` | `get_enabled` | yes | see vendor-getters.txt |
| `led_cli` | `set_enabled` | no | setter / writes (rule 4) |
| `led_cli` | `update_system_state` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_band_conflict` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_cat_browser` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_cat_text` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_conn_state` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_ddtm` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_e911` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_lifetime_cnt` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_network_bars` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_network_cellid` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_network_dbm` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_network_ecio` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_network_extender` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_network_lac` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_network_operator` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_network_reg` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_network_rsrp` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_network_rsrq` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_network_rssi` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_network_service` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_network_sidnid` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_network_sinr` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_network_snr` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_network_time` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_pip_tone` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_signal_cdma` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_signal_gsm` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_signal_hdr` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_signal_lte` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_signal_wcdma` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_sim_act` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_uati` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_voice_cellid` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_voice_lac` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_voice_mccmnc` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_voice_plmn_name` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_voice_reg` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_voice_roam` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_voice_svc` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_wap_push` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_reject_code` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_pco_event` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `get_call_throttle_status` | yes | see vendor-getters.txt |
| `modem2_cli` | `get_reject_cause_code` | yes | see vendor-getters.txt |
| `modem2_cli` | `1xrtt_get_ext_timer` | yes | see vendor-getters.txt |
| `modem2_cli` | `1xrtt_set_ext_timer` | no | setter / writes (rule 4) |
| `modem2_cli` | `active_band_get` | yes | see vendor-getters.txt |
| `modem2_cli` | `band_class_get_enabled` | yes | see vendor-getters-prompted.txt |
| `modem2_cli` | `band_class_set_enabled` | no | setter / writes (rule 4) |
| `modem2_cli` | `bsr_get_timers` | yes | see vendor-getters.txt |
| `modem2_cli` | `bsr_set_timers` | no | setter / writes (rule 4) |
| `modem2_cli` | `cai_get` | yes | see vendor-getters.txt |
| `modem2_cli` | `cai_set` | no | setter / writes (rule 4) |
| `modem2_cli` | `ca_get_enabled` | yes | see vendor-getters.txt |
| `modem2_cli` | `ca_set_enabled` | no | setter / writes (rule 4) |
| `modem2_cli` | `ca_bands_get_enabled` | yes | see vendor-getters-prompted.txt |
| `modem2_cli` | `ca_bands_set_enabled` | no | setter / writes (rule 4) |
| `modem2_cli` | `ca_tri_bands_set_enabled` | no | setter / writes (rule 4) |
| `modem2_cli` | `ca_tri_bands_get_enabled` | yes | see vendor-getters-prompted.txt |
| `modem2_cli` | `get_autonomous_gap_enabled` | yes | see vendor-getters.txt |
| `modem2_cli` | `set_autonomous_gap_enabled` | no | setter / writes (rule 4) |
| `modem2_cli` | `check_lte_ca_status` | yes | see vendor-getters.txt |
| `modem2_cli` | `ddtm_get` | yes | see vendor-getters.txt |
| `modem2_cli` | `ddtm_set` | no | setter / writes (rule 4) |
| `modem2_cli` | `efs_read` | no | waiting for your OK on the path list |
| `modem2_cli` | `efs_write` | no | setter / writes (rule 4) |
| `modem2_cli` | `efs_read_large` | no | waiting for your OK on the path list |
| `modem2_cli` | `efs_write_large` | no | setter / writes (rule 4) |
| `modem2_cli` | `efs_delete` | no | action / changes state |
| `modem2_cli` | `ehrpd_get_enabled` | yes | see vendor-getters.txt |
| `modem2_cli` | `ehrpd_set_enabled` | no | setter / writes (rule 4) |
| `modem2_cli` | `ehrpd_get_state` | yes | see vendor-getters.txt |
| `modem2_cli` | `ehrpd_set_state` | no | setter / writes (rule 4) |
| `modem2_cli` | `emergency_get_mode` | yes | see vendor-getters.txt |
| `modem2_cli` | `factory_reset` | no | setter / writes (rule 4) |
| `modem2_cli` | `get_diag_info` | yes | see vendor-getters-prompted.txt |
| `modem2_cli` | `get_refurb_info` | yes | see vendor-getters.txt |
| `modem2_cli` | `get_act_date` | yes | see vendor-getters.txt |
| `modem2_cli` | `get_sup_tech` | yes | see vendor-getters.txt |
| `modem2_cli` | `ims_reg_set_delay` | no | setter / writes (rule 4) |
| `modem2_cli` | `ims_pres_get_config` | yes | see vendor-getters.txt |
| `modem2_cli` | `ims_pres_set_config` | no | setter / writes (rule 4) |
| `modem2_cli` | `ims_lvc_get_enabled` | yes | see vendor-getters.txt |
| `modem2_cli` | `ims_lvc_set_enabled` | no | setter / writes (rule 4) |
| `modem2_cli` | `ims_set_sip_timer` | no | setter / writes (rule 4) |
| `modem2_cli` | `ims_get_sip_data` | yes | see vendor-getters.txt |
| `modem2_cli` | `ims_set_sip_data` | no | setter / writes (rule 4) |
| `modem2_cli` | `ims_get_sms_data` | yes | see vendor-getters.txt |
| `modem2_cli` | `unlock_carrier` | no | action / changes state |
| `modem2_cli` | `lbs_get` | yes | see vendor-getters.txt |
| `modem2_cli` | `lbs_set` | no | setter / writes (rule 4) |
| `modem2_cli` | `lifetime_counters_get` | yes | see vendor-getters.txt |
| `modem2_cli` | `lifetime_counters_update` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `lte_band_get_enabled` | yes | see vendor-getters-prompted.txt |
| `modem2_cli` | `lte_band_set_enabled` | no | setter / writes (rule 4) |
| `modem2_cli` | `lte_band_get_prior` | yes | see vendor-getters.txt |
| `modem2_cli` | `lte_band_set_prior` | no | setter / writes (rule 4) |
| `modem2_cli` | `pdn_get_ext_params` | yes | see vendor-getters-prompted.txt |
| `modem2_cli` | `pdn_set_ext_params` | no | setter / writes (rule 4) |
| `modem2_cli` | `powersave` | no | action / changes state |
| `modem2_cli` | `prof_get_tech` | yes | see vendor-getters-prompted.txt |
| `modem2_cli` | `prof_get_pri_tech` | yes | see vendor-getters-prompted.txt |
| `modem2_cli` | `prof_get_cust_tech` | yes | see vendor-getters-prompted.txt |
| `modem2_cli` | `prof_set_tech` | no | setter / writes (rule 4) |
| `modem2_cli` | `prof_set_pri_tech` | no | setter / writes (rule 4) |
| `modem2_cli` | `prof_set_cust_tech` | no | setter / writes (rule 4) |
| `modem2_cli` | `prof_get_act_tech` | yes | see vendor-getters-prompted.txt |
| `modem2_cli` | `prof_set_act_tech` | no | setter / writes (rule 4) |
| `modem2_cli` | `roam_get_enabled` | yes | see vendor-getters.txt |
| `modem2_cli` | `roam_set_enabled` | no | setter / writes (rule 4) |
| `modem2_cli` | `roam_get_intl_enabled` | yes | see vendor-getters.txt |
| `modem2_cli` | `roam_set_intl_enabled` | no | setter / writes (rule 4) |
| `modem2_cli` | `roam_get_eri` | yes | see vendor-getters.txt |
| `modem2_cli` | `mdn_min_set` | no | setter / writes (rule 4) |
| `modem2_cli` | `mip_get_settings` | yes | see vendor-getters.txt |
| `modem2_cli` | `mip_set_settings` | no | setter / writes (rule 4) |
| `modem2_cli` | `mip_get_profile` | yes | see vendor-getters.txt |
| `modem2_cli` | `mip_set_profile` | no | setter / writes (rule 4) |
| `modem2_cli` | `mns_get_info` | yes | see vendor-getters.txt |
| `modem2_cli` | `mns_start_scan` | no | action / changes state |
| `modem2_cli` | `mns_get_list` | yes | see vendor-getters.txt |
| `modem2_cli` | `mns_clear_list` | no | action / changes state |
| `modem2_cli` | `mns_set_oper` | no | setter / writes (rule 4) |
| `modem2_cli` | `mns_validate` | no | "validate": not clearly read-only |
| `modem2_cli` | `network_attach` | no | action / changes state |
| `modem2_cli` | `radio_is_enabled` | yes | see vendor-getters.txt |
| `modem2_cli` | `radio_set_enabled` | no | setter / writes (rule 4) |
| `modem2_cli` | `sd_config_get_setting` | yes | see vendor-getters-prompted.txt |
| `modem2_cli` | `sd_config_set_setting` | no | setter / writes (rule 4) |
| `modem2_cli` | `sim_enable_pin` | no | action / changes state |
| `modem2_cli` | `sim_unlock_pin` | no | action / changes state |
| `modem2_cli` | `sim_unlock_puk` | no | action / changes state |
| `modem2_cli` | `sim_change_pin` | no | action / changes state |
| `modem2_cli` | `sim_pin_get_status` | yes | see vendor-getters.txt |
| `modem2_cli` | `sim_get_carrier` | yes | see vendor-getters.txt |
| `modem2_cli` | `sim_get_gid1` | yes | see vendor-getters.txt |
| `modem2_cli` | `sim_get_gid2` | yes | see vendor-getters.txt |
| `modem2_cli` | `sim_get_iccid` | yes | see vendor-getters.txt |
| `modem2_cli` | `get_imsi` | yes | see vendor-getters.txt |
| `modem2_cli` | `sim_get_mnc_length` | yes | see vendor-getters.txt |
| `modem2_cli` | `validate_spc` | no | action / changes state |
| `modem2_cli` | `volte_get_amr_mode` | yes | see vendor-getters.txt |
| `modem2_cli` | `volte_set_amr_mode` | no | setter / writes (rule 4) |
| `modem2_cli` | `volte_get_amr_wb_mode` | yes | see vendor-getters.txt |
| `modem2_cli` | `volte_set_amr_wb_mode` | no | setter / writes (rule 4) |
| `modem2_cli` | `volte_get_dcmo_timer` | yes | see vendor-getters.txt |
| `modem2_cli` | `volte_set_dcmo_timer` | no | setter / writes (rule 4) |
| `modem2_cli` | `volte_get_dcmo_tdelay` | yes | see vendor-getters.txt |
| `modem2_cli` | `volte_set_dcmo_tdelay` | no | setter / writes (rule 4) |
| `modem2_cli` | `volte_get_enabled` | yes | see vendor-getters.txt |
| `modem2_cli` | `volte_set_enabled` | no | setter / writes (rule 4) |
| `modem2_cli` | `volte_get_hd_voice_enab` | yes | see vendor-getters.txt |
| `modem2_cli` | `volte_set_hd_voice_enab` | no | setter / writes (rule 4) |
| `modem2_cli` | `volte_get_hys` | yes | see vendor-getters.txt |
| `modem2_cli` | `volte_set_hys` | no | setter / writes (rule 4) |
| `modem2_cli` | `volte_get_rcl_max_entries` | yes | see vendor-getters.txt |
| `modem2_cli` | `volte_set_rcl_max_entries` | no | setter / writes (rule 4) |
| `modem2_cli` | `volte_get_sess_config` | yes | see vendor-getters.txt |
| `modem2_cli` | `volte_set_sess_config` | no | setter / writes (rule 4) |
| `modem2_cli` | `volte_get_silent_redial` | yes | see vendor-getters.txt |
| `modem2_cli` | `volte_set_silent_redial` | no | setter / writes (rule 4) |
| `modem2_cli` | `volte_get_src_thttle` | yes | see vendor-getters.txt |
| `modem2_cli` | `volte_set_src_thttle` | no | setter / writes (rule 4) |
| `modem2_cli` | `volte_get_tlte_911fail` | yes | see vendor-getters.txt |
| `modem2_cli` | `volte_set_tlte_911fail` | no | setter / writes (rule 4) |
| `modem2_cli` | `enable_data_call` | no | action / changes state |
| `modem2_cli` | `get_world_mode_enabled` | yes | see vendor-getters.txt |
| `modem2_cli` | `get_pco_event` | yes | see vendor-getters.txt |
| `modem2_cli` | `get_apn_from_database` | yes | see vendor-getters-prompted.txt |
| `modem2_cli` | `get_custom_apn_from_database` | yes | see vendor-getters-prompted.txt |
| `modem2_cli` | `set_custom_apn_to_database` | no | setter / writes (rule 4) |
| `modem2_cli` | `validate_apn` | no | "validate": not clearly read-only |
| `modem2_cli` | `run_raw_command` | no | action / changes state |
| `modem2_cli` | `set_lte_wifi_coex` | no | setter / writes (rule 4) |
| `modem2_cli` | `update_sim_status` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_wwan_state` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_roam_state` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_roam_ctrl` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_oprt_mode` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_mns` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_plmn_mode` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_plmn_name` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `update_oper_name` | no | simulated event / private API (rule 4) |
| `modem2_cli` | `rc_to_str` | no | meta |
| `modem2_cli` | `is_running` | yes | see vendor-getters.txt |
| `modem2_cli` | `call_get_status` | yes | see vendor-getters-prompted.txt |
| `modem2_cli` | `call_start` | no | action / changes state |
| `modem2_cli` | `call_stop` | no | action / changes state |
| `modem2_cli` | `active_tech_get` | yes | see vendor-getters.txt |
| `modem2_cli` | `enabled_tech_get` | yes | see vendor-getters.txt |
| `modem2_cli` | `enabled_tech_set` | no | setter / writes (rule 4) |
| `modem2_cli` | `get_oper_info` | yes | see vendor-getters.txt |
| `modem2_cli` | `get_network_time` | yes | see vendor-getters.txt |
| `modem2_cli` | `get_cached_time` | yes | see vendor-getters.txt |
| `modem2_cli` | `get_carrier_unlock` | yes | see vendor-getters.txt |
| `modem2_cli` | `get_info` | yes | see vendor-getters.txt |
| `modem2_cli` | `get_state` | yes | see vendor-getters-prompted.txt |
| `modem2_cli` | `get_signal` | yes | see vendor-getters.txt |
| `modem2_cli` | `get_voice_signal` | yes | see vendor-getters.txt |
| `modem2_cli` | `get_service_info` | yes | see vendor-getters.txt |
| `modem2_cli` | `sim_get_status` | yes | see vendor-getters.txt |
| `modem2_cli` | `validate_home` | no | "validate": not clearly read-only |
| `modem_at_server_cli` | `print_error_codes` | no | meta (no device data) |
| `modem_at_server_cli` | `is_running` | no | meta (no device data) |
| `modem_at_server_cli` | `update_request` | no | simulated event / private API (rule 4) |
| `modem_at_server_cli` | `update_usage_info` | no | simulated event / private API (rule 4) |
| `modem_at_server_cli` | `update_omadm_info` | no | simulated event / private API (rule 4) |
| `msgbus_cli` | `print_error_codes` | no | meta (no device data) |
| `msgbus_cli` | `print_channels` | yes | see vendor-getters.txt |
| `msgbus_cli` | `MsgBusGet` | yes | see vendor-getters.txt |
| `msgbus_cli` | `MsgBusSetInt32` | no | setter / writes (rule 4) |
| `msgbus_cli` | `MsgBusSetInt64` | no | setter / writes (rule 4) |
| `msgbus_cli` | `MsgBusSetFloat` | no | setter / writes (rule 4) |
| `msgbus_cli` | `MsgBusSetDouble` | no | setter / writes (rule 4) |
| `msgbus_cli` | `MsgBusSetString` | no | setter / writes (rule 4) |
| `msgbus_cli` | `MsgBusSetBinary` | no | setter / writes (rule 4) |
| `msgbus_cli` | `MsgBusPublishInt32` | no | setter / writes (rule 4) |
| `msgbus_cli` | `MsgBusPublishInt64` | no | setter / writes (rule 4) |
| `msgbus_cli` | `MsgBusPublishFloat` | no | setter / writes (rule 4) |
| `msgbus_cli` | `MsgBusPublishDouble` | no | setter / writes (rule 4) |
| `msgbus_cli` | `MsgBusPublishString` | no | setter / writes (rule 4) |
| `msgbus_cli` | `MsgBusPublishBinary` | no | setter / writes (rule 4) |
| `msgbus_cli` | `MsgBus_Subscribe` | no | listen-only, but not in the allowed list for this step |
| `msgbus_cli` | `MsgBusDelete` | no | action / changes state |
| `nua_cli` | (all) | never | rule 4: never run |
| `omadm_cli` | (all) | never | rule 4: never run |
| `omadm_ipl_cli` | (all) | never | rule 4: never run |
| `powersave_cli` | `print_error_codes` | no | meta (no device data) |
| `powersave_cli` | `is_running` | no | meta (no device data) |
| `powersave_cli` | `wakelock_acquire` | no | action / changes state |
| `powersave_cli` | `wakelock_release` | no | action / changes state |
| `powersave_cli` | `get_settings` | yes | see vendor-getters.txt |
| `powersave_cli` | `set_settings` | no | setter / writes (rule 4) |
| `powersave_cli` | `dsm_state_change` | no | action / changes state |
| `powersave_cli` | `charger_det_change` | no | action / changes state |
| `powersave_cli` | `battery_det_change` | no | action / changes state |
| `powersave_cli` | `battery_state_change` | no | action / changes state |
| `powersave_cli` | `battery_temperature_state_change` | no | action / changes state |
| `powersave_cli` | `pwr_button_state_change` | no | action / changes state |
| `powersave_cli` | `ip_client_change` | no | action / changes state |
| `router2_cli` | `is_running` | no | meta (no device data) |
| `router2_cli` | `log` | no | writes internal state to the system log |
| `router2_cli` | `get_ippt` | yes | see vendor-getters.txt |
| `router2_cli` | `set_ippt` | no | setter / writes (rule 4) |
| `router2_cli` | `get_dns_name` | yes | see vendor-getters.txt |
| `router2_cli` | `set_dns_name` | no | setter / writes (rule 4) |
| `router2_cli` | `get_port_forwarding` | yes | see vendor-getters.txt |
| `router2_cli` | `set_port_forwarding` | no | setter / writes (rule 4) |
| `router2_cli` | `get_port_fwd_list` | yes | see vendor-getters.txt |
| `router2_cli` | `set_port_fwd_list` | no | setter / writes (rule 4) |
| `router2_cli` | `start_v4` | no | action / changes state |
| `router2_cli` | `stop_v4` | no | action / changes state |
| `router2_cli` | `start_v6` | no | action / changes state |
| `router2_cli` | `stop_v6` | no | action / changes state |
| `router2_cli` | `start_v4_apps` | no | action / changes state |
| `router2_cli` | `stop_v4_apps` | no | action / changes state |
| `router2_cli` | `start_v4_dm` | no | action / changes state |
| `router2_cli` | `stop_v4_dm` | no | action / changes state |
| `router2_cli` | `start_v6_dm` | no | action / changes state |
| `router2_cli` | `stop_v6_dm` | no | action / changes state |
| `router2_cli` | `get_port_filter` | yes | see vendor-getters.txt |
| `router2_cli` | `set_port_filter` | no | setter / writes (rule 4) |
| `router2_cli` | `get_port_filter_list` | yes | see vendor-getters.txt |
| `router2_cli` | `set_port_filter_list` | no | setter / writes (rule 4) |
| `router2_cli` | `get_vpnpassthrough` | yes | see vendor-getters.txt |
| `router2_cli` | `set_vpnpassthrough` | no | setter / writes (rule 4) |
| `router2_cli` | `get_http_redirect` | yes | see vendor-getters.txt |
| `router2_cli` | `set_http_redirect` | no | setter / writes (rule 4) |
| `router2_cli` | `dns_lookup` | no | makes a DNS query over LTE; not a getter |
| `router2_cli` | `get_mtu` | yes | see vendor-getters.txt |
| `router2_cli` | `set_mtu` | no | setter / writes (rule 4) |
| `router2_cli` | `get_v6_fw` | yes | see vendor-getters.txt |
| `router2_cli` | `set_v6_fw` | no | setter / writes (rule 4) |
| `router2_cli` | `get_lan_settings` | yes | see vendor-getters.txt |
| `router2_cli` | `set_lan_settings` | no | setter / writes (rule 4) |
| `router2_cli` | `get_dmz` | yes | see vendor-getters.txt |
| `router2_cli` | `set_dmz` | no | setter / writes (rule 4) |
| `router2_cli` | `get_dhcp` | yes | see vendor-getters.txt |
| `router2_cli` | `set_dhcp` | no | setter / writes (rule 4) |
| `router2_cli` | `get_manual_dns` | yes | see vendor-getters.txt |
| `router2_cli` | `set_manual_dns` | no | setter / writes (rule 4) |
| `router2_cli` | `get_pub_access` | yes | see vendor-getters.txt |
| `router2_cli` | `set_pub_access` | no | setter / writes (rule 4) |
| `router2_cli` | `modify_br_ifc` | no | action / changes state |
| `router2_cli` | `get_active_dns` | yes | see vendor-getters.txt |
| `router2_cli` | `add_rout_mon_service` | no | action / changes state |
| `router2_cli` | `list_rout_mon_service` | yes | see vendor-getters.txt |
| `router2_cli` | `blacklist_update_event` | no | simulated event / private API (rule 4) |
| `router2_cli` | `whitelist_update_event` | no | simulated event / private API (rule 4) |
| `router2_cli` | `macfilter_update_event` | no | simulated event / private API (rule 4) |
| `router2_cli` | `internet_blocked_update_event` | no | simulated event / private API (rule 4) |
| `router2_cli` | `statistics_update_event` | no | simulated event / private API (rule 4) |
| `router2_cli` | `get_ntp` | yes | see vendor-getters.txt |
| `router2_cli` | `set_ntp` | no | setter / writes (rule 4) |
| `router2_cli` | `handle_usb` | no | action / changes state |
| `settings_cli` | `print_error_codes` | no | meta (no device data) |
| `settings_cli` | `is_running` | no | meta (no device data) |
| `settings_cli` | `get_features` | yes | see vendor-getters.txt |
| `settings_cli` | `get_branding_info` | yes | see vendor-getters.txt |
| `settings_cli` | `backup` | no | action / changes state |
| `settings_cli` | `restore` | no | action / changes state |
| `settings_cli` | `factory_reset` | no | setter / writes (rule 4) |
| `sms_cli` | `print_error_codes` | no | meta (no device data) |
| `sms_cli` | `rc_to_str` | no | meta |
| `sms_cli` | `is_running` | no | meta (no device data) |
| `sms_cli` | `get_unread` | yes | see vendor-getters.txt |
| `sms_cli` | `get_list` | yes | see vendor-getters-prompted.txt |
| `sms_cli` | `set_state` | no | setter / writes (rule 4) |
| `sms_cli` | `read` | no | may mark the SMS as read; ask first |
| `sms_cli` | `send` | no | action / changes state |
| `sms_cli` | `delete` | no | action / changes state |
| `sms_cli` | `ab_get_list` | yes | see vendor-getters.txt |
| `sms_cli` | `ab_get_entry` | no | address book is empty (count 0) |
| `sms_cli` | `ab_get_entry_addr` | no | address book is empty |
| `sms_cli` | `ab_get_entry_name` | no | address book is empty |
| `sms_cli` | `ab_edit_entry` | no | action / changes state |
| `sms_cli` | `ab_add_entry` | no | action / changes state |
| `sms_cli` | `ab_del_entry` | no | action / changes state |
| `usb_cli` | `print_error_codes` | no | meta (no device data) |
| `usb_cli` | `is_running` | no | meta (no device data) |
| `usb_cli` | `get_usb_tethering_feature` | yes | see vendor-getters.txt |
| `usb_cli` | `get_tether_enabled` | yes | see vendor-getters.txt |
| `usb_cli` | `set_tether_enabled` | no | setter / writes (rule 4) |
| `usb_cli` | `set_tether_enabled_temp` | no | setter / writes (rule 4) |
| `usb_cli` | `get_config` | yes | see vendor-getters.txt |
| `usb_cli` | `get_state` | yes | see vendor-getters.txt |
| `usb_cli` | `start` | no | action / changes state |
| `usb_cli` | `stop` | no | action / changes state |
| `usb_cli` | `restart` | no | action / changes state |
| `usb_cli` | `mode_switch` | no | action / changes state |
| `vpn_cli` | `print_error_codes` | no | meta (no device data) |
| `vpn_cli` | `is_running` | no | meta (no device data) |
| `vpn_cli` | `get_auto_connect` | yes | see vendor-getters.txt |
| `vpn_cli` | `set_auto_connect` | no | setter / writes (rule 4) |
| `vpn_cli` | `get_settings` | yes | see vendor-getters.txt |
| `vpn_cli` | `set_settings` | no | setter / writes (rule 4) |
| `vpn_cli` | `connect_vpn` | no | action / changes state |
| `vpn_cli` | `disconnect_vpn` | no | action / changes state |
| `vpn_cli` | `get_connection_statistics` | yes | see vendor-getters.txt |
| `vpn_cli` | `update_wan_connection_status` | no | simulated event / private API (rule 4) |
| `vpn_cli` | `update_connected_clients_count` | no | simulated event / private API (rule 4) |
| `vpn_cli` | `list_vpn_mon_service` | yes | see vendor-getters.txt |
| `vpn_cli` | `validate_openvpn_config_files` | no | action / changes state |
| `watchdog_cli` | `print_error_codes` | no | meta (no device data) |
| `watchdog_cli` | `is_running` | no | meta (no device data) |
| `watchdog_cli` | `get_settings` | yes | see vendor-getters.txt |
| `watchdog_cli` | `set_settings` | no | setter / writes (rule 4) |
| `watchdog_cli` | `application_register` | no | action / changes state |
| `watchdog_cli` | `application_send_keep_alive` | no | action / changes state |
| `watchdog_cli` | `get_app_list` | yes | see vendor-getters.txt |
| `webui_cli` | `get_network_technology_list` | yes | see vendor-getters.txt |
| `webui_cli` | `print_error_codes` | no | meta (no device data) |
| `webui_cli` | `is_running` | no | meta (no device data) |
| `webui_cli` | `get_login_settings` | yes | see vendor-getters.txt |
| `webui_cli` | `set_login_settings` | no | setter / writes (rule 4) |
| `webui_cli` | `get_log_settings` | yes | see vendor-getters.txt |
| `webui_cli` | `set_log_settings` | no | setter / writes (rule 4) |
| `webui_cli` | `get_i18n_settings` | yes | see vendor-getters.txt |
| `webui_cli` | `set_i18n_settings` | no | setter / writes (rule 4) |
| `webui_cli` | `get_app_properties` | yes | see vendor-getters-prompted.txt |
| `webui_cli` | `get_app_features` | yes | see vendor-getters.txt |
| `webui_cli` | `get_app_configurations` | yes | see vendor-getters.txt |
| `webui_cli` | `get_uri_data` | yes | see vendor-getters-prompted.txt |
| `webui_cli` | `get_common_features` | yes | see vendor-getters.txt |
| `webui_cli` | `get_common_configurations` | yes | see vendor-getters.txt |
| `webui_cli` | `get_supported_languages` | yes | see vendor-getters.txt |
| `webui_cli` | `get_welcome_notification` | yes | see vendor-getters.txt |
| `webui_cli` | `set_welcome_notification` | no | setter / writes (rule 4) |
| `webui_cli` | `get_advanced_settings` | yes | see vendor-getters.txt |
| `webui_cli` | `set_advanced_settings` | no | setter / writes (rule 4) |
| `webui_cli` | `get_customer_support_info` | yes | see vendor-getters.txt |
| `webui_cli` | `get_external_uri_data` | no | may fetch from the internet; not run |
| `webui_cli` | `get_site_title_info` | yes | see vendor-getters.txt |
| `webui_cli` | `get_software_update_status` | yes | see vendor-getters.txt |
| `webui_cli` | `su_check_for_update` | no | simulated event / private API (rule 4) |
| `webui_cli` | `su_start_install` | no | action / changes state |
| `webui_cli` | `su_potpone_install` | no | action / changes state |
| `webui_cli` | `get_software_installation_duration` | yes | see vendor-getters.txt |
| `webui_cli` | `get_country_code` | yes | see vendor-getters-prompted.txt |
| `webui_cli` | `set_diagnostic_alert_choice` | no | setter / writes (rule 4) |
| `webui_cli` | `get_network_technology_options` | yes | see vendor-getters.txt |
| `wifi_cli` | `error_codes` | no | meta (no device data) |
| `wifi_cli` | `is_running` | no | meta (no device data) |
| `wifi_cli` | `log` | no | writes internal state to the system log |
| `wifi_cli` | `get_ap_profile` | yes | see vendor-getters-prompted.txt |
| `wifi_cli` | `set_ap_profile` | no | setter / writes (rule 4) |
| `wifi_cli` | `start_wps` | no | action / changes state |
| `wifi_cli` | `stop_wps` | no | action / changes state |
| `wifi_cli` | `get_caps` | yes | see vendor-getters.txt |
| `wifi_cli` | `get_mac` | yes | see vendor-getters-prompted.txt |
| `wifi_cli` | `get_enable` | yes | see vendor-getters.txt |
| `wifi_cli` | `set_enable` | no | setter / writes (rule 4) |
| `wifi_cli` | `get_ap_enable` | yes | see vendor-getters.txt |
| `wifi_cli` | `set_ap_enable` | no | setter / writes (rule 4) |
| `wifi_cli` | `get_sta_enable` | yes | see vendor-getters.txt |
| `wifi_cli` | `set_sta_enable` | no | setter / writes (rule 4) |
| `wifi_cli` | `get_sta_list` | yes | see vendor-getters.txt |
| `wifi_cli` | `set_all_profiles` | no | setter / writes (rule 4) |
| `wifi_cli` | `set_profile_status` | no | setter / writes (rule 4) |
| `wifi_cli` | `set_ap_settings` | no | setter / writes (rule 4) |
| `wifi_cli` | `get_ap_settings` | yes | see vendor-getters.txt |
| `wifi_cli` | `get_settings` | yes | see vendor-getters.txt |
| `wifi_cli` | `set_settings` | no | setter / writes (rule 4) |
| `wifi_cli` | `add_mac_blist` | no | action / changes state |
| `wifi_cli` | `get_pindex` | yes | see vendor-getters-prompted.txt |
| `wifi_cli` | `set_wifi_ps` | no | setter / writes (rule 4) |
| `wifi_cli` | `get_band_status` | yes | see vendor-getters-prompted.txt |
| `xmldata_cli` | `print_error_codes` | no | meta (no device data) |
| `xmldata_cli` | `getstring` | no | needs a file and XPath; config XML can be read directly |
