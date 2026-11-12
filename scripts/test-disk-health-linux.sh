#!/usr/bin/env bash
# Dedicated Debian root harness. Build as a regular user with Go's pinned toolchain:
#   go test -tags disk_health_fixture -c -o /path/to/agent-fixture ./internal/agent
# On an authorized disposable test Agent, with v1.1.2 active:
#   sudo bash scripts/test-disk-health-linux.sh /path/to/agent-fixture [all|fault|stall]
# The only block-device writes are to a new loop-backed device in a private
# mount namespace. The fixture Agent runs as nobody and talks over loopback.
set -Eeuo pipefail
[[ $(id -u) == 0 ]] || { echo 'root required'; exit 2; }
if [[ ${1:-} != --private ]]; then
  [[ $# -ge 1 && $# -le 2 && -f $1 && ! -L $1 && -x $1 ]] || { echo 'provide the built fixture binary'; exit 2; }
  exec unshare --mount --propagation private -- bash "$(realpath -- "$0")" --private "$(readlink /proc/self/ns/mnt)" "$(realpath -- "$1")" "${2:-all}"
fi
[[ $# == 4 && $(readlink /proc/self/ns/mnt) != "$2" && $(findmnt -n -o PROPAGATION /) == private ]] || exit 2
binary=$3
scenario=$4
[[ $scenario == all || $scenario == fault || $scenario == stall ]] || exit 2
[[ $(systemctl is-active 404-probe-agent.service) == active && $(/usr/local/bin/404-probe-agent version) == v1.1.2 ]] || exit 2
printf 'KERNEL_RELEASE %s\n' "$(uname -r)"
for utility in losetup dmsetup mkfs.ext4 e2fsck debugfs dumpe2fs mount umount blockdev modprobe setpriv python3; do command -v "$utility" >/dev/null; done

work=$(mktemp -d /var/lib/r48-disk.XXXXXXXX)
name=r48-${work##*.}
image=$work/image
dev=/dev/mapper/$name
mnt=$work/mount
bind=$work/bind
control=$work/control
dir=$mnt/agent
loop= fixture_pid= created=0 suspended=0 step_number=0 sectors=0
uid=$(id -u nobody)
gid=$(id -g nobody)

snapshot() {
  local paths=(/run/404-probe /etc/404-probe /usr/local/bin/404-probe-agent /usr/local/sbin/404-probe-install /var/lib/404-probe /etc/404-probe/agent.env /etc/systemd/system/404-probe-agent.service)
  local p
  shopt -s nullglob
  paths+=(/var/lib/404-probe* /etc/404-probe/* /etc/systemd/system/404-probe*)
  for p in "${paths[@]}"; do
    if [[ -e $p || -L $p ]]; then stat -c '%n %f %u %g %i' -- "$p"; else printf '%s absent\n' "$p"; fi
  done | LC_ALL=C sort -u
  systemctl show 404-probe-agent.service 404-probe-agent-updater.service 404-probe-security-collect.timer \
    -p LoadState -p ActiveState -p SubState -p MainPID -p NRestarts -p ExecMainStartTimestampMonotonic
  /usr/local/bin/404-probe-agent version
}

owned_loop() {
  [[ $loop =~ ^/dev/loop[0-9]+$ && -f $image && ! -L $image ]] &&
    [[ $(losetup --list --noheadings --output BACK-FILE -- "$loop") == "$image" ]]
}

replace_table() {
  [[ $created == 1 && $name == r48-"${work##*.}" ]] && owned_loop || return 2
  dmsetup suspend --noflush "$name" || return
  suspended=1
  dmsetup reload "$name" --table "$1" || return
  dmsetup resume "$name" || return
  suspended=0
}

cleanup() {
  local rc=$? cleanup_rc=0
  trap - EXIT
  trap '' HUP INT TERM
  set +e
  if [[ $created == 1 ]] && owned_loop; then
    if [[ $suspended == 1 ]]; then dmsetup resume "$name" || cleanup_rc=1; suspended=0; fi
    replace_table "0 $sectors linear $loop 0" || cleanup_rc=1
  fi
  if [[ -n $fixture_pid ]]; then
    kill "$fixture_pid" 2>/dev/null
    wait "$fixture_pid" 2>/dev/null
  fi
  if mountpoint -q "$bind"; then umount -- "$bind" || cleanup_rc=1; fi
  if mountpoint -q "$mnt"; then umount -- "$mnt" || cleanup_rc=1; fi
  if [[ $created == 1 ]]; then dmsetup remove --retry "$name" || cleanup_rc=1; fi
  if [[ -n $loop ]]; then
    if owned_loop; then losetup -d "$loop" || cleanup_rc=1; else cleanup_rc=1; fi
    if [[ -n $(losetup -j "$image") ]]; then cleanup_rc=1; fi
  fi
  if dmsetup info "$name" >/dev/null 2>&1 || mountpoint -q "$mnt" || mountpoint -q "$bind"; then cleanup_rc=1; fi
  snapshot > "$work/after" || cleanup_rc=1
  if cmp -s "$work/before" "$work/after"; then
    printf 'REAL_SNAPSHOT differences=0 sha256=%s\n' "$(sha256sum "$work/after" | cut -d ' ' -f 1)"
  else
    echo 'STOP: real installation snapshot differs'
    cleanup_rc=1
  fi
  if [[ -f $work/fixture.log ]]; then cat "$work/fixture.log"; fi
  if [[ $cleanup_rc == 0 && $work =~ ^/var/lib/r48-disk\.[A-Za-z0-9]+$ && ! -L $work ]]; then
    rm -rf --one-file-system -- "$work" || cleanup_rc=1
  fi
  if [[ $cleanup_rc != 0 || -e $work ]]; then echo 'STOP: fixture cleanup incomplete'; exit 67; fi
  echo 'CLEANUP mounts=0 dm=0 loop=0 temp=0'
  exit "$rc"
}

trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
snapshot > "$work/before"
chmod 0755 "$work"
mkdir "$mnt" "$bind" "$control"
chown "$uid:$gid" "$control"
chmod 0700 "$control"
install -m 0755 -- "$binary" "$work/fixture"
modprobe dm-flakey
modprobe dm-delay
truncate -s 128M -- "$image"
loop=$(losetup --find --show -- "$image")
owned_loop
sectors=$(blockdev --getsz "$loop")
[[ $sectors == 262144 ]]
linear="0 $sectors linear $loop 0"
if dmsetup info "$name" >/dev/null 2>&1; then exit 2; fi
dmsetup create "$name" --table "$linear"
created=1
[[ -b $dev && $(blockdev --getsz "$dev") == "$sectors" ]]
mkfs.ext4 -q -F "$dev"
mount -t ext4 -o errors=remount-ro "$dev" "$mnt"
mkdir "$dir"
chown "$uid:$gid" "$dir"

setpriv --reuid "$uid" --regid "$gid" --clear-groups env R48_DISK_FIXTURE_CONTROL="$control" R48_DISK_FIXTURE_DIR="$dir" \
  "$work/fixture" -test.run '^TestDiskHealthFaultFixture$' -test.v -test.timeout 8m >"$work/fixture.log" 2>&1 &
fixture_pid=$!

send() {
  step_number=$((step_number+1))
  request="$step_number $1"
  printf '%s\n' "$request" > "$control/command.new"
  chmod 0644 "$control/command.new"
  mv -f -- "$control/command.new" "$control/command"
}
result() {
  python3 - "$control/result" "$request" "${1:-60}" <<'PY'
import json, sys, time
from pathlib import Path
path, command, timeout = sys.argv[1:]
deadline = time.monotonic() + int(timeout)
while time.monotonic() < deadline:
    try:
        result = json.loads(Path(path).read_text())
        if result['command'] == command:
            print(json.dumps(result, separators=(',', ':')))
            break
    except FileNotFoundError:
        pass
    time.sleep(.10)
else:
    raise SystemExit('timed out waiting for fixture probe')
PY
}
check_result() {
  python3 - "$1" "$2" "${3:-}" "${4:-}" <<'PY'
import json, sys
data, status, reasons, absent = sys.argv[1:]
result = json.loads(data)
disk = result['disk']
assert disk['status'] == status, result
assert set(filter(None, reasons.split(','))).issubset(disk.get('reasons', [])), result
assert not set(filter(None, absent.split(','))).intersection(disk.get('reasons', [])), result
if reasons == 'read_only' and result['command'].endswith(' readonly'):
    assert result['erofs'] and disk['read_only_super'], result
PY
}
check_mount_options() {
  python3 - "$1" "$2" "$3" <<'PY'
import sys
path, mount_option, super_option = sys.argv[1:]
with open('/proc/self/mountinfo') as mounts:
    for line in mounts:
        before, after = line.split(' - ', 1)
        if before.split()[4] == path:
            per_mount = before.split()[5].split(',')[0]
            options = after.split()[2]
            superblock = options.split(',')[0]
            print(f'MOUNT_OPTIONS mount={per_mount} super={superblock} super_options={options}')
            if (mount_option == '*' or per_mount == mount_option) and (super_option == '*' or superblock == super_option):
                sys.exit(0)
            sys.exit(66)
raise SystemExit(66)
PY
}

send probe; healthy=$(result); check_result "$healthy" ok; printf 'BASELINE %s\n' "$healthy"
if [[ $scenario != stall ]]; then
mount --bind "$mnt" "$bind"
mount -o remount,bind,ro "$bind"
check_mount_options "$bind" ro rw
umount -- "$bind"
mount -o remount,ro "$mnt"
check_mount_options "$mnt" ro ro
send readonly; readonly_result=$(result); check_result "$readonly_result" error read_only
printf 'PASS manual read-only %s\n' "$readonly_result"
mount -o remount,rw "$mnt"
send probe; check_result "$(result)" ok

rm -- "$dir/.disk-probe"
mkdir -- "$mnt/cold-metadata"
touch -- "$mnt/cold-metadata/known-file"
sync -f "$mnt"
umount -- "$mnt"
# A data write error alone can leave ext4 mounted read-write. Also fail a
# cold directory-block read, keeping the journal and superblock writable so
# ext4 can record the metadata error and apply errors=remount-ro itself.
block=$(debugfs -R 'blocks /cold-metadata' "$dev" 2>/dev/null | xargs)
block_size=$(dumpe2fs -h "$dev" 2>/dev/null | awk '$1=="Block" && $2=="size:" {print $3}')
[[ $block =~ ^[1-9][0-9]*$ && $block_size =~ ^(1024|2048|4096)$ ]]
block_sectors=$((block_size/512))
fault_start=$((block*block_sectors))
fault_end=$((fault_start+block_sectors))
((fault_start > 0 && fault_end < sectors))
mount -t ext4 -o errors=remount-ro "$dev" "$mnt"
fault_time=$(date +%s)
replace_table "0 $sectors error"
send probe; eio=$(result); printf 'DEVICE_ERROR %s\n' "$eio"
check_result "$eio" error write_eio
replace_table "0 $fault_start linear $loop 0
$fault_start $block_sectors error
$fault_end $((sectors-fault_end)) linear $loop $fault_end"
if stat -- "$mnt/cold-metadata/known-file" > /dev/null 2> "$work/metadata-error"; then
  echo 'cold ext4 directory read unexpectedly succeeded'; exit 1
fi
LC_ALL=C grep -q 'Input/output error' "$work/metadata-error"
for _ in {1..5}; do
  send probe; eio=$(result); printf 'KERNEL_ERROR %s\n' "$eio"
  if check_result "$eio" error write_eio,ext4_errors 2>/dev/null; then break; fi
  sleep 1
done
check_result "$eio" error write_eio,ext4_errors
send readonly; after_eio=$(result); printf 'AFTER_EXT4_ERROR %s\n' "$after_eio"
actual_mount=$(check_mount_options "$mnt" '*' '*')
printf '%s\n' "$actual_mount"
if [[ $actual_mount == *' super=ro '* ]]; then
  check_result "$after_eio" error write_eio,ext4_errors,read_only
else
  check_result "$after_eio" error write_eio,ext4_errors read_only
fi
kernel_dev=$(basename -- "$(readlink -f -- "$dev")")
if [[ $kernel_dev =~ ^dm-[0-9]+$ ]]; then
  journalctl -k --since "@$fault_time" --no-pager -o cat --grep "EXT4-fs.*\($kernel_dev\)" |
    awk 'tolower($0) ~ /remounting.*read.only/ {print "KERNEL_EXT4 announced remount read-only"} tolower($0) ~ /abort/ {print "KERNEL_EXT4 announced abort"}' || true
fi
replace_table "$linear"
umount -- "$mnt"
fsck_rc=0
e2fsck -fy "$dev" > "$work/fsck.log" 2>&1 || fsck_rc=$?
[[ $fsck_rc == 0 || $fsck_rc == 1 ]]
mount -t ext4 -o errors=remount-ro "$dev" "$mnt"
[[ -d $dir ]] || { mkdir "$dir"; chown "$uid:$gid" "$dir"; }
rm -f -- "$mnt/cold-metadata/known-file"
rmdir -- "$mnt/cold-metadata"
send probe; check_result "$(result)" error write_eio,ext4_errors
send probe; check_result "$(result)" error ext4_errors write_eio
send age; check_result "$(result)" ok
echo 'PASS device I/O error, ext4 delta, reported read-only matches superblock, two successful probes and expired ext4 window'
fi

if [[ $scenario != fault ]]; then
replace_table "0 $sectors delay $loop 0 0 $loop 0 35000 $loop 0 0"
send probe
python3 - "$control/report" <<'PY'
import json, sys, time
from pathlib import Path
report = Path(sys.argv[1])
start = time.monotonic()
sequences = set()
while time.monotonic() < start + 45:
    try:
        value = json.loads(report.read_text())
    except FileNotFoundError:
        time.sleep(.05)
        continue
    if 'stalled' in value['disk'].get('reasons', []):
        sequences.add(value['sequence'])
        if len(sequences) >= 3:
            print(f'PASS stalled fsync; accepted reports={len(sequences)} elapsed={time.monotonic()-start:.2f}s')
            break
    time.sleep(.05)
else:
    raise SystemExit('stalled probe did not reach continuing reports')
PY
replace_table "$linear"
check_result "$(result 150)" ok slow
send probe; check_result "$(result)" ok
fi
[[ $(find "$dir" -mindepth 1 -maxdepth 1 -printf '%f %m\n') == '.disk-probe 600' ]]
send stop
wait "$fixture_pid"
fixture_pid=
echo 'PASS recovered probe and worker exit, only mode-0600 probe file present'
