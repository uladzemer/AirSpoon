#!/system/bin/sh
L=/data/local/tmp/skwnew/swap.log
exec >$L 2>&1
set -x
date
svc wifi disable; svc bluetooth disable; sleep 5
lsmod | grep -E 'skw|Module'
rmmod skw; sleep 1; rmmod skw_sdio; sleep 1
lsmod | grep -E 'skw|Module'
echo -n /data/local/tmp/skwnew/fw > /sys/module/firmware_class/parameters/path
insmod /data/local/tmp/skwnew/skw_sdio_lite.ko
insmod /data/local/tmp/skwnew/skw_sdio.ko
sleep 4
insmod /data/local/tmp/skwnew/skw.ko
sleep 2
lsmod | grep -E 'skw|Module'
head -4 /proc/skwifi/profile
svc wifi enable
sleep 25
ip -4 addr show wlan0
svc bluetooth enable
echo DONE
