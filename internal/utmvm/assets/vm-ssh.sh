#!/bin/sh
# vm-ssh: the OpenSSH server in a Linux guest, and one public key allowed in.
# Pushed and run as root by `irgo-winvm vm-ssh-create`; -Remove is
# `vm-ssh-delete`. Idempotent: on a VM that has it, every line says "ok" and
# the last says nothing changed. The same arguments as vm-ssh.ps1.
#
# What it opens, and to whom: TCP 22, on every address the guest has. The
# image has no firewall, so whatever can reach the guest can reach the port.
#
# What it sets in sshd's configuration, in a file of its own: no password is
# accepted. Only the key gets in.
user=dev
keyfile=
remove=0
while [ $# -gt 0 ]; do
  case "$1" in
    -User) user=$2; shift 2 ;;
    -KeyFile) keyfile=$2; shift 2 ;;
    -Remove) remove=1; shift ;;
    *) echo "ssh: unknown argument $1"; exit 2 ;;
  esac
done
fail() { echo "$1"; exit 1; }

# Read before the image's 60-cloudimg-settings.conf: sshd keeps the first
# value it sees for a keyword.
conf=/etc/ssh/sshd_config.d/00-irgo-winvm.conf

# Ubuntu since 22.10 listens with ssh.socket and starts sshd on the first
# connection; where there is no such unit, the service listens itself.
if systemctl cat ssh.socket >/dev/null 2>&1; then listener=ssh.socket; else listener=ssh.service; fi

listening() { ss -Hltn 'sport = :22' | grep -q .; }

if [ "$remove" -eq 1 ]; then
  systemctl disable --now ssh.socket ssh.service >/dev/null 2>&1
  # Sessions outlive the service that accepted them.
  pkill -x sshd
  pkill -x sshd-session
  n=0
  for f in /root/.ssh/authorized_keys /home/*/.ssh/authorized_keys; do
    if [ -e "$f" ]; then rm -f "$f"; n=$((n + 1)); fi
  done
  rm -f "$conf"
  if listening; then fail 'ssh: something still listens on port 22'; fi
  echo "ssh: removed (sshd stopped and disabled, $n authorized_keys file(s), $conf); the server stays installed"
  exit 0
fi

changed=0

home=$(getent passwd "$user" | cut -d: -f6)
[ -n "$home" ] || fail "account: there is no account $user"
[ -d "$home" ] || fail "account: $user has no home directory at $home, so nowhere to keep its keys"
echo "account: ok ($user, home $home)"

[ -x /usr/sbin/sshd ] || fail 'openssh server: /usr/sbin/sshd is not there, and this script does not install it'
echo 'openssh server: ok (installed)'

# An image sealed without host keys gives each clone its own.
if ls /etc/ssh/ssh_host_*_key >/dev/null 2>&1; then
  echo 'host keys: ok'
else
  ssh-keygen -A >/dev/null || fail 'host keys: ssh-keygen -A could not make them'
  changed=$((changed + 1))
  echo 'host keys: generated now (ssh-keygen -A)'
fi

want='PasswordAuthentication no
KbdInteractiveAuthentication no'
if [ -f "$conf" ] && [ "$(cat "$conf")" = "$want" ]; then
  echo "passwords: ok (refused, by $conf)"
else
  printf '%s\n' "$want" > "$conf" || fail "passwords: could not write $conf"
  chmod 644 "$conf"
  # A server already running read the old configuration.
  systemctl try-restart ssh.service >/dev/null 2>&1
  changed=$((changed + 1))
  echo "passwords: refused now (by $conf)"
fi
# What sshd would do, not what the file says: a file sshd does not read
# changes nothing.
mkdir -p /run/sshd
if ! /usr/sbin/sshd -T 2>/dev/null | grep -qix 'passwordauthentication no'; then
  fail "passwords: $conf is written, and sshd -T does not report PasswordAuthentication no"
fi

# The key, compared by type and key and not by comment.
[ -n "$keyfile" ] && [ -f "$keyfile" ] || fail "key: no key file at ${keyfile:-(none given)}"
key2() { printf '%s\n' "$1" | awk '{ print $1 " " $2 }'; }
line=$(grep -v '^[[:space:]]*$' "$keyfile" | head -n 1)
dir=$home/.ssh
keys=$dir/authorized_keys
present=0
if [ -f "$keys" ]; then
  while IFS= read -r have || [ -n "$have" ]; do
    if [ "$(key2 "$have")" = "$(key2 "$line")" ]; then present=1; fi
  done < "$keys"
fi
if [ "$present" -eq 1 ]; then
  echo "key: ok (already in $keys)"
else
  mkdir -p "$dir" || fail "key: could not make $dir"
  # A last line with no newline would run into this one.
  if [ -s "$keys" ] && [ -n "$(tail -c 1 "$keys")" ]; then echo >> "$keys"; fi
  printf '%s\n' "$line" >> "$keys" || fail "key: could not write $keys"
  changed=$((changed + 1))
  echo "key: authorized now (in $keys)"
fi
# sshd ignores keys that anyone but their owner can write.
chown -R "$user:$(id -gn "$user")" "$dir" && chmod 700 "$dir" && chmod 600 "$keys" ||
  fail "key: could not give $dir to $user alone"
rm -f "$keyfile"

if systemctl is-enabled --quiet "$listener" 2>/dev/null && systemctl is-active --quiet "$listener"; then
  echo "sshd: ok ($listener enabled and active)"
else
  systemctl enable --now "$listener" >/dev/null 2>&1 || fail "sshd: could not enable and start $listener"
  changed=$((changed + 1))
  echo "sshd: started now, enabled ($listener)"
fi

if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q '^Status: active'; then
  echo 'firewall: ufw is active and was left as it is'
else
  echo 'firewall: none (ufw is inactive), so port 22 is open to whatever can reach the guest'
fi

n=0
until listening; do
  n=$((n + 1))
  [ "$n" -le 40 ] || fail 'sshd: enabled, and nothing listens on port 22 after 20 s'
  sleep 0.5
done

if [ "$changed" -eq 0 ]; then echo 'ssh: already on, nothing changed'; else echo "ssh: on ($changed change(s))"; fi
