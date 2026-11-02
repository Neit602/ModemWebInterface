#!/bin/sh
# probe-modem.sh is the second, focused survey: how the stock software talks to the modem,
# which vendor command-line tools exist and what they can report.
# Read-only: it lists files, shows which process holds which device, prints init scripts,
# and runs vendor *_cli tools with no arguments (that prints their help). It sends nothing to the modem.
#
# Usage:   adb push probe-modem.sh /tmp/
#          adb shell sh /tmp/probe-modem.sh /tmp/probe-modem.txt
#          adb pull /tmp/probe-modem.txt

OUT=${1:-/tmp/probe-modem.txt}
exec > "$OUT" 2>&1

sec() { echo; echo "===== $1"; }

# BusyBox from 2015 uses "timeout -t SECS", newer versions use "timeout SECS".
if timeout -t 1 true 2>/dev/null; then T="timeout -t 5"
elif timeout 1 true 2>/dev/null; then T="timeout 5"
else T=""; fi

sec "version"
date
cat /opt/nvtl/etc/mifios-version 2>/dev/null
uptime

sec "who holds modem devices"
for p in /proc/[0-9]*; do
  for f in "$p"/fd/*; do
    t=$(readlink "$f" 2>/dev/null) || continue
    case "$t" in
      /dev/smd*|/dev/at_*|/dev/ttyHS*|/dev/ttyGS*|/dev/diag|/dev/rmnet*|/dev/wwan*|/dev/qcqmi*|/dev/cdc-wdm*)
        echo "$t  <-  $(cat "$p/comm" 2>/dev/null) (pid ${p#/proc/})";;
    esac
  done
done | sort | uniq

sec "AT plumbing: init scripts"
for f in /etc/init.d/port_bridge /etc/init.d/start_atfwd_daemon; do
  echo "--- $f"; cat "$f" 2>/dev/null
done
for f in $(grep -l -E 'modem_at_server|modem2d|port_bridge' /etc/init.d/* /opt/nvtl/etc/init.d/* 2>/dev/null | sort -u); do
  echo "--- $f"; head -60 "$f"
done
ls -l /etc/rc3.d 2>/dev/null

sec "vendor files"
ls -la /opt/nvtl /opt/nvtl/bin /opt/nvtl/etc /opt/nvtl/lib 2>/dev/null
ls -la /opt/nvtl/tmp 2>/dev/null | head -60
ls -la /opt/nvtl/data 2>/dev/null

sec "vendor CLI help (each tool run with no arguments)"
for c in /opt/nvtl/bin/*_cli /usr/bin/QCMAP_CLI; do
  [ -x "$c" ] || continue
  case "$(basename "$c")" in
    *omadm*|*fota*|*factory*|*reset*|*upi*|*update*|*flash*|*nua*|*sua*|*bckrst*) echo "--- $c (skipped)"; continue;;
  esac
  echo "--- $c"
  $T "$c" </dev/null 2>&1 | head -120
done

sec "dhcp leases"
ls -la /opt/nvtl/tmp/router2 2>/dev/null
head -60 /opt/nvtl/tmp/router2/dhcpd.leases 2>/dev/null
cat /proc/net/arp

sec "ipv6 exposure"
ip -6 addr 2>/dev/null
ip -6 route 2>/dev/null
ip6tables -S 2>/dev/null | head -60
iptables -S 2>/dev/null | head -60

sec "time sync"
ps w 2>/dev/null | grep -Ei '[n]tp|[c]hrony|[t]imed'

sec "sqlite databases (tables only)"
for db in /opt/nvtl/data/sms/sms_inbox.db /opt/nvtl/data/dmdb/dmdb.db /opt/nvtl/data/modem2/apn_validation.db; do
  [ -f "$db" ] || continue
  echo "--- $db"
  sqlite3 "$db" '.tables' 2>&1 | head -20
done

echo
echo "===== done"
