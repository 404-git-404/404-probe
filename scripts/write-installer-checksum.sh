#!/usr/bin/env bash

set -Eeuo pipefail

[[ $# -eq 2 ]] || {
  printf 'usage: %s INSTALLER SHA256SUMS\n' "$0" >&2
  exit 2
}

readonly installer="$1"
readonly checksums="$2"
[[ "$(basename "${installer}")" == install.sh ]] || {
  printf 'installer checksum input must be named install.sh\n' >&2
  exit 1
}

checksum_output="$(sha256sum "${installer}")"
checksum="${checksum_output%%[[:space:]]*}"
[[ "${checksum}" =~ ^[[:xdigit:]]{64}$ ]] || {
  printf 'sha256sum returned a malformed installer digest\n' >&2
  exit 1
}

# Always emit the strict GNU text-manifest form consumed by install.sh. In
# particular, do not preserve MSYS/Git Bash's default `*filename` marker.
printf '%s  install.sh\n' "${checksum}" >>"${checksums}"
