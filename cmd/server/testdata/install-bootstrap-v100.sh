# Extracted verbatim from v1.0.0 install.sh, commit 6399bcf796bef8a13fd7768253f28ecdc73e1c8b.
canonical_version() {
  [[ "$1" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]
}

target_version() {
  local version="${PROBE_404_VERSION:-${DEFAULT_VERSION}}"
  canonical_version "${version}" || die "target version must use canonical vX.Y.Z form"
  printf '%s\n' "${version}"
}

bootstrap_latest_installer() (
  local effective latest release_base temporary_directory installer checksum_line status
  if [[ -n "${PROBE_404_VERSION:-}" ]]; then
    latest="${PROBE_404_VERSION}"
    canonical_version "${latest}" || die "explicit target version must use canonical vX.Y.Z form"
  else
    effective="$(curl_with_retry \
      --output /dev/null --write-out '%{url_effective}' "https://github.com/${REPOSITORY}/releases/latest")" \
      || die "could not resolve the latest stable release"
    latest="${effective##*/}"
    canonical_version "${latest}" || die "latest stable release returned a non-canonical version"
    [[ "${effective}" == "https://github.com/${REPOSITORY}/releases/tag/${latest}" ]] \
      || die "latest stable release redirected outside the official repository"
  fi
  release_base="https://github.com/${REPOSITORY}/releases/download/${latest}"
  temporary_directory="$(mktemp -d)"
  trap 'rm -rf -- "${temporary_directory}"' EXIT
  installer="${temporary_directory}/install.sh"
  download_to_file "${installer}" \
    "${release_base}/install.sh" \
    "https://raw.githubusercontent.com/${REPOSITORY}/${latest}/install.sh" \
    || die "could not download the ${latest} installer from official entries"
  download_release_asset "${latest}" SHA256SUMS "${temporary_directory}/SHA256SUMS" \
    || die "could not download the ${latest} checksum manifest from official entries"
  checksum_line="$(grep -E '^[[:xdigit:]]{64}  install\.sh$' "${temporary_directory}/SHA256SUMS" || true)"
  [[ "$(printf '%s\n' "${checksum_line}" | grep -c .)" -eq 1 ]] || die "latest release does not authenticate install.sh exactly once"
  (cd "${temporary_directory}" && printf '%s\n' "${checksum_line}" | sha256sum --check --strict -) \
    || die "latest release installer checksum verification failed"
  if LC_ALL=C grep -q $'\r' "${installer}"; then
    die "latest release installer contains carriage returns"
  fi
  bash -n "${installer}" || die "latest release installer failed syntax validation"
  PROBE_404_INSTALLER_BOOTSTRAPPED=1 PROBE_404_BOOTSTRAP_SHA256SUMS="${temporary_directory}/SHA256SUMS" \
    PROBE_404_VERSION="${latest}" bash "${installer}" "$@"
  status=$?
  exit "${status}"
)
