#!/usr/bin/env bash

set -Eeuo pipefail

[[ "${EUID}" -eq 0 ]] || { printf 'run as root\n' >&2; exit 1; }
[[ $# -eq 2 ]] || { printf 'usage: %s INSTALLER AGENT_BINARY\n' "$0" >&2; exit 1; }

source_installer="$1"
source_agent="$2"
fixture="$(mktemp -d /tmp/404-probe-selector-existing.XXXXXX)"
trap 'rm -rf -- "${fixture}"' EXIT
chmod 0755 "${fixture}"

bin_directory="${fixture}/bin"
config_directory="${fixture}/etc/404-probe"
unit="${fixture}/etc/404-probe-agent.service"
installer="${fixture}/installer"
agent="${bin_directory}/404-probe-agent"
helper="${bin_directory}/404-probe-install"
metadata="${config_directory}/selector-order.json"
sing_box_config="${fixture}/sing-box.json"

install -d -o root -g root -m 0755 "${bin_directory}" "${fixture}/etc"
install -d -o root -g 404-probe -m 0750 "${config_directory}"
install -o root -g root -m 0755 "${source_agent}" "${agent}"
sed \
  -e "s|readonly INSTALL_HELPER=\"/usr/local/sbin/404-probe-install\"|readonly INSTALL_HELPER=\"${helper}\"|g" \
  -e "s|readonly AGENT_BINARY=\"/usr/local/bin/404-probe-agent\"|readonly AGENT_BINARY=\"${agent}\"|g" \
  -e "s|readonly CONFIG_DIRECTORY=\"/etc/404-probe\"|readonly CONFIG_DIRECTORY=\"${config_directory}\"|g" \
  -e "s|readonly AGENT_UNIT=\"/etc/systemd/system/404-probe-agent.service\"|readonly AGENT_UNIT=\"${unit}\"|g" \
  "${source_installer}" >"${installer}"
chmod 0755 "${installer}"

printf '%s\n' \
  '[Service]' \
  'User=404-probe' \
  'Group=404-probe' \
  "EnvironmentFile=${config_directory}/agent.env" \
  "ExecStart=${agent} run --config ${config_directory}/agent.env" >"${unit}"
chmod 0644 "${unit}"
install -o root -g 404-probe -m 0640 /dev/null "${config_directory}/agent.env"
printf '%s\n' '{"outbounds":[{"type":"selector","tag":"alpha"},{"type":"direct","tag":"ignored"},{"type":"selector","tag":"beta"}]}' >"${sing_box_config}"
chmod 0600 "${sing_box_config}"

PROBE_404_INSTALLER_BOOTSTRAPPED=1 PROBE_404_VERSION=v0.9.2 "${installer}" selector-order "${sing_box_config}"
[[ "$(<"${metadata}")" == '{"selectors":["alpha","beta"]}' ]]
[[ "$(stat -c '%U:%G:%a' "${metadata}")" == 'root:404-probe:640' ]]
runuser -u 404-probe -- test -r "${metadata}"
! runuser -u 404-probe -- test -w "${metadata}"
! runuser -u 404-probe -- test -w "${config_directory}"
grep -Fq 'selector-order' "${helper}"

printf '%s\n' '["old"]' >"${metadata}"
chown root:404-probe "${metadata}"
chmod 0640 "${metadata}"
printf '%s\n' '#!/usr/bin/env bash' 'printf old-helper\\n' >"${helper}"
chmod 0755 "${helper}"
metadata_before="$(sha256sum "${metadata}" | awk '{print $1}')"
helper_before="$(sha256sum "${helper}" | awk '{print $1}')"
install -d -m 0755 "${fixture}/fail-bin"
printf '%s\n' '#!/bin/sh' 'exit 99' >"${fixture}/fail-bin/bash"
chmod 0755 "${fixture}/fail-bin/bash"

if PATH="${fixture}/fail-bin:${PATH}" PROBE_404_INSTALLER_BOOTSTRAPPED=1 PROBE_404_VERSION=v0.9.2 /bin/bash "${installer}" selector-order "${sing_box_config}" >/dev/null 2>&1; then
  printf 'failure fixture unexpectedly succeeded\n' >&2
  exit 1
fi
[[ "$(sha256sum "${metadata}" | awk '{print $1}')" == "${metadata_before}" ]]
[[ "$(sha256sum "${helper}" | awk '{print $1}')" == "${helper_before}" ]]

printf 'selector-order existing-Agent success and rollback fixtures passed; cleanup=%s\n' "${fixture}"
