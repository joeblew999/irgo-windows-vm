#!/bin/sh
# vm-linux-check: is this the machine vm-create -os linux promises? Run as root
# through the guest agent once it answers, which is before cloud-init has
# finished. One line for each thing checked; the first that is wrong ends it
# with exit 1. -New is a VM on its first boot, where SSH must be off; on one
# that has lived, vm-ssh-create may have turned it on, and it is only said.
user=dev
new=0
while [ $# -gt 0 ]; do
  case "$1" in
    -User) user=$2; shift 2 ;;
    -New) new=1; shift ;;
    *) echo "check: unknown argument $1"; exit 2 ;;
  esac
done
fail() { echo "$1"; exit 1; }

# Waits for the first boot's work to end: the account, the packages and the
# commands in user-data. Anything but "done" with exit 0 is a seed that did
# not apply cleanly. A clone of the golden image has cloud-init off: its
# first boot was the image's.
if [ -e /etc/cloud/cloud-init.disabled ]; then
  echo "cloud-init: off (a clone of the golden image, whose seal turned it off)"
else
  cloud-init status --wait >/dev/null 2>&1
  rc=$?
  state=$(cloud-init status 2>/dev/null | head -n 1)
  [ "$rc" -eq 0 ] || fail "cloud-init: $state, exit $rc; /var/log/cloud-init-output.log in the guest says why"
  echo "cloud-init: ok ($state)"
fi

id "$user" >/dev/null 2>&1 || fail "account: there is no account $user, so cloud-init did not apply the seed"
sudo -u "$user" sudo -n true 2>/dev/null || fail "account: $user cannot use sudo without a password"
[ "$(passwd -S "$user" | cut -d' ' -f2)" = L ] || fail "account: $user has a password set, and should have none"
echo "account: ok ($user, sudo without a password, password locked)"

if ! ss -Hltn 'sport = :22' | grep -q .; then
  echo "ssh: ok (off: nothing listens on port 22 until vm-ssh-create)"
elif [ "$new" -eq 1 ]; then
  fail "ssh: something listens on port 22, and the seed turns the image's sshd off"
else
  echo "ssh: on (port 22 is listening; vm-ssh-delete turns it off)"
fi

echo "disk: ok (/ is $(df -h --output=size / | tail -n 1 | tr -d ' '), $(df -h --output=used / | tail -n 1 | tr -d ' ') used)"
echo "system: $(. /etc/os-release && echo "$PRETTY_NAME"), kernel $(uname -r), hostname $(hostname), machine-id $(cut -c1-12 /etc/machine-id)"
