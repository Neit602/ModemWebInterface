#!/bin/sh
# probe.sh collects information about the MiFi so the panel can be wired to real data.
# It changes nothing and sends nothing to the modem: it only reads files and the process list.
#
# Usage:   adb push probe.sh /tmp/
#          adb shell sh /tmp/probe.sh /tmp/probe.txt
#          adb pull /tmp/probe.txt

OUT=${1:-/tmp/probe.txt}
exec > "$OUT" 2>&1

sec() { echo; echo "===== $1"; }
has() { command -v "$1" >/dev/null 2>&1; }

sec "system"
date
uname -a
cat /proc/version
id
head -30 /proc/cpuinfo
cat /etc/os-release 2>/dev/null
cat /etc/version* /etc/*release* 2>/dev/null | head -20
head -5 /proc/meminfo
cat /proc/cmdline

sec "storage"
df -h 2>/dev/null || df
mount
cat /proc/mtd 2>/dev/null
for d in /data /usrdata /persist /cache /tmp /var /opt /etc; do
  [ -d "$d" ] && [ -w "$d" ] && echo "writable: $d"
done

sec "listening ports"
netstat -tlnp 2>/dev/null || netstat -tln 2>/dev/null || cat /proc/net/tcp
netstat -ulnp 2>/dev/null | head -40

sec "processes"
ps w 2>/dev/null || ps

sec "network"
ip addr 2>/dev/null || ifconfig -a
ip route 2>/dev/null || route -n
cat /proc/net/dev
cat /proc/net/arp

sec "firewall"
if has iptables; then
  iptables -S 2>/dev/null | head -150
  iptables -t nat -S 2>/dev/null | head -60
fi

sec "devices (possible AT ports)"
ls -la /dev | grep -Ei 'smd|at_|tty|qcqmi|cdc-wdm|diag|mhi|wwan|rmnet|qmi'

sec "power supply"
for d in /sys/class/power_supply/*; do
  [ -d "$d" ] || continue
  echo "-- $d"
  for f in type status capacity present online temp voltage_now current_now health charge_type; do
    [ -f "$d/$f" ] && echo "$f=$(cat "$d/$f" 2>/dev/null)"
  done
done

sec "dhcp leases"
for f in /opt/nvtl/tmp/router2/dhcpd.leases /var/lib/misc/dnsmasq.leases /tmp/dnsmasq.leases /var/run/dnsmasq.leases /data/dnsmasq.leases; do
  [ -f "$f" ] && echo "$f: $(wc -l < "$f") lines"
done
find / -xdev -name '*.leases' 2>/dev/null | head -20
ps w 2>/dev/null | grep -i [d]nsmasq

sec "web server"
ls -la /etc/lighttpd* /www /var/www /usr/www /data/www 2>/dev/null | head -80
ps w 2>/dev/null | grep -Ei '[l]ighttpd|[n]ginx|[h]ttpd|[u]httpd|[w]ebs'

sec "init and autostart"
ls -la /etc/init.d 2>/dev/null
ls /etc/rc*.d 2>/dev/null | head -150
cat /etc/inittab 2>/dev/null | head -40

sec "tools"
for b in busybox dsm_cli mifi_cli atcmd atcli at_cmd qmicli uqmi microcom socat nc wget curl lighttpd dnsmasq hostapd adbd dropbear telnetd iptables sqlite3 python3 lua; do
  p=$(command -v "$b" 2>/dev/null) && echo "$b: $p"
done
busybox 2>/dev/null | tail -n +2 | tr -s ' \n\t' ' ' | head -c 4000
echo
ls /usr/bin /usr/sbin /bin /sbin 2>/dev/null | tr '\n' ' ' | head -c 12000
echo

sec "vendor files"
ls -la /opt /data /persist /usr/local 2>/dev/null | head -100
find / -xdev \( -iname '*inseego*' -o -iname '*novatel*' -o -iname '*mifi*' -o -iname '*dsm*' -o -iname '*.db' \) 2>/dev/null | grep -v '^/proc' | head -80

echo
echo "===== done"
