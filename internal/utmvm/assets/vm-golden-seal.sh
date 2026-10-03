#!/bin/sh
# vm-golden-seal: make a Linux VM fit to be copied. Pushed and run as root by
# `irgo-winvm vm-golden-create`, one step at a time, so the host can measure
# the disk between steps and say which one is running. Idempotent: every step
# on a VM already sealed changes nothing that matters. One line per fact,
# "name: value", which the host reads.
#
#  clean  what makes a machine itself goes, so each clone makes its own: the
#         machine-id (and the DHCP identity systemd derives from it), the SSH
#         host keys, every authorized key. The network is matched by name,
#         not by the MAC cloud-init wrote, since every clone has a new MAC.
#         cloud-init has had its one boot: its state goes and it is turned
#         off, because a clone has no seed CD and would otherwise wait for
#         one and write the network again.
#  trim   UTM passes discard=unmap to QEMU, so fstrim may punch holes in the
#         raw disk.img on the host. Whether it does is what the host measures
#         around this step.
steps='facts clean trim'
step=$1
case " $steps " in
  *" $step "*) ;;
  *) echo "seal: unknown step '$step' (one of: $steps)"; exit 2 ;;
esac
fail() { echo "$1"; exit 1; }
netplan=/etc/netplan/01-irgo-winvm.yaml

case "$step" in
  facts)
    echo "system: $(. /etc/os-release && echo "$PRETTY_NAME"), kernel $(uname -r)"
    echo "machine-id: $(cat /etc/machine-id)"
    echo "host-keys: $(ls /etc/ssh/ssh_host_*_key 2>/dev/null | wc -l)"
    echo "authorized-keys: $(ls /root/.ssh/authorized_keys /home/*/.ssh/authorized_keys 2>/dev/null | wc -l)"
    if [ -e /etc/cloud/cloud-init.disabled ]; then echo 'cloud-init: off'; else echo 'cloud-init: on'; fi
    echo "netplan: $(ls /etc/netplan/ | tr '\n' ' ')"
    echo "used: $(df -B1 --output=used / | tail -n 1 | tr -d ' ')"
    ;;

  clean)
    # Nothing listens, and nothing lets anyone in. A VM that had
    # vm-ssh-create run on it loses its keys and its configuration too.
    systemctl disable --now ssh.socket ssh.service >/dev/null 2>&1
    rm -f /etc/ssh/ssh_host_* /etc/ssh/sshd_config.d/00-irgo-winvm.conf \
      /root/.ssh/authorized_keys /home/*/.ssh/authorized_keys
    if ls /etc/ssh/ssh_host_* /home/*/.ssh/authorized_keys >/dev/null 2>&1; then
      fail 'ssh: a host key or an authorized key is still there'
    fi
    echo 'ssh: off; host keys, authorized keys and 00-irgo-winvm.conf removed'

    printf '%s\n' 'network:' '  version: 2' '  ethernets:' '    primary:' \
      '      match:' '        name: "en*"' '      dhcp4: true' '      dhcp-identifier: mac' > "$netplan" ||
      fail "network: could not write $netplan"
    chmod 600 "$netplan"
    rm -f /etc/netplan/50-cloud-init.yaml
    netplan generate || fail "network: netplan generate refused $netplan"
    echo "network: any en* interface by DHCP, identified by its MAC ($netplan); cloud-init's file removed"

    cloud-init clean --logs >/dev/null 2>&1 || fail 'cloud-init: cloud-init clean failed'
    touch /etc/cloud/cloud-init.disabled || fail 'cloud-init: could not turn it off'
    echo 'cloud-init: state and logs removed, off for every clone'

    apt-get clean
    journalctl --rotate >/dev/null 2>&1
    journalctl --vacuum-time=1s >/dev/null 2>&1
    echo 'caches: apt cleaned, journal emptied'

    # Empty, not removed: systemd makes a new one at each clone's first boot.
    : > /etc/machine-id || fail 'machine-id: could not empty /etc/machine-id'
    if [ -f /var/lib/dbus/machine-id ] && [ ! -L /var/lib/dbus/machine-id ]; then rm -f /var/lib/dbus/machine-id; fi
    [ ! -s /etc/machine-id ] || fail 'machine-id: /etc/machine-id is not empty'
    echo 'machine-id: emptied; each clone makes its own'
    ;;

  trim)
    # The ext4 filesystems only: on /boot/efi (vfat) FITRIM fails with an
    # I/O error on a VirtIO disk (measured 3 Oct 2026), and it is 100 MB.
    for m in $(findmnt -rn -t ext4 -o TARGET); do
      out=$(fstrim -v "$m" 2>&1) || fail "trim: fstrim $m failed: $out"
      echo "trim: $out"
    done
    ;;
esac
