#!/usr/bin/env bash
# Release gate tests use a private mock Git/Go PATH; no real refs or assets change.
set -Eeuo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
temporary="$(mktemp -d "${TMPDIR:-/tmp}/404-release-gates.XXXXXX")"
temporary="$(realpath -- "${temporary}")"
[[ "${temporary}" == "$(realpath -- "${TMPDIR:-/tmp}")"/404-release-gates.* ]] || exit 1
trap '[[ -d "${temporary}" && ! -L "${temporary}" ]] && rm -rf -- "${temporary}"' EXIT
mkdir "${temporary}/bin"
cat >"${temporary}/bin/git" <<'SH'
#!/usr/bin/env bash
case "$1" in
  status) printf '%s' "${FIXTURE_DIRTY:-}" ;;
  show-ref) [[ "${FIXTURE_TAG:-0}" == 1 ]] ;;
  rev-parse)
    if [[ "$3" == HEAD ]]; then printf '%s\n' "${FIXTURE_HEAD}";
    else printf '%s\n' "${FIXTURE_TAG_COMMIT:-${FIXTURE_HEAD}}"; fi ;;
  show) printf '#!/usr/bin/env bash\nreadonly DEFAULT_VERSION="%s"\n' "${FIXTURE_INSTALLER_VERSION:-v1.0.8}" ;;
  *) exit 69 ;;
esac
SH
# Mock go: "go version" reports FIXTURE_GO_VERSION; "go build" either stops at the compiler gate
# (default) or, with FIXTURE_ELF_MODE=1, writes an empty file that "go version -m" describes using
# FIXTURE_ELF_GO / FIXTURE_ELF_REVISION / FIXTURE_ELF_MODIFIED; "go run" marks the metadata step.
cat >"${temporary}/bin/go" <<'SH'
#!/usr/bin/env bash
case "$1" in
  version)
    if [[ "${2:-}" == -m ]]; then
      printf '%s: go%s\n\tpath\t404-probe/cmd/mock\n\tbuild\tvcs.revision=%s\n\tbuild\tvcs.modified=%s\n' \
        "$3" "${FIXTURE_ELF_GO:-${FIXTURE_EXPECTED_GO}}" "${FIXTURE_ELF_REVISION:-${FIXTURE_HEAD}}" "${FIXTURE_ELF_MODIFIED:-false}"
    else
      printf 'go version go%s linux/amd64\n' "${FIXTURE_GO_VERSION:-${FIXTURE_EXPECTED_GO}}"
    fi ;;
  build)
    if [[ "${FIXTURE_ELF_MODE:-0}" != 1 ]]; then printf '%s\n' 'COMPILE_GATE_REACHED'; exit 71; fi
    while [[ $# -gt 0 ]]; do [[ "$1" == -o ]] && { : >"$2"; break; }; shift; done ;;
  run) printf '%s\n' 'METADATA_GATE_REACHED'; exit 72 ;;
  *) exit 69 ;;
esac
SH
chmod 0700 "${temporary}/bin/git" "${temporary}/bin/go"
export PATH="${temporary}/bin:${PATH}"
export FIXTURE_HEAD=0123456789abcdef0123456789abcdef01234567
# The expected compiler is whatever go.mod pins; nothing here hard-codes a version.
FIXTURE_EXPECTED_GO="$(sed -n 's/^toolchain go\([0-9][0-9.]*\)[[:space:]]*$/\1/p' "${root}/go.mod" | head -n 1)"
[[ -n "${FIXTURE_EXPECTED_GO}" ]] || { printf 'go.mod has no toolchain line\n' >&2; exit 1; }
export FIXTURE_EXPECTED_GO
export PROBE_404_RELEASE_OUTPUT="${temporary}/assets"
check() {
  local name="$1" expected="$2"; shift 2
  if env "$@" bash "${root}/scripts/build-release-assets.sh" >"${temporary}/result" 2>&1; then
    printf 'unexpected successful mock build: %s\n' "${name}" >&2; exit 1
  fi
  grep -Fq "${expected}" "${temporary}/result" || { cat "${temporary}/result"; exit 1; }
  printf '%s: %s\n' "${name}" "${expected}"
}
check 'dirty rejects explicit commit' 'clean git worktree' FIXTURE_DIRTY=' M install.sh' PROBE_404_RELEASE_COMMIT="${FIXTURE_HEAD}"
check 'default still requires tag' 'release tag v1.0.8 does not exist' PROBE_404_RELEASE_COMMIT=
check 'short commit rejected' 'full clean HEAD commit' PROBE_404_RELEASE_COMMIT=0123456
check 'wrong commit rejected' 'full clean HEAD commit' PROBE_404_RELEASE_COMMIT=ffffffffffffffffffffffffffffffffffffffff
check 'conflicting existing tag rejected' 'existing release tag does not match' FIXTURE_TAG=1 FIXTURE_TAG_COMMIT=ffffffffffffffffffffffffffffffffffffffff PROBE_404_RELEASE_COMMIT="${FIXTURE_HEAD}"
check 'wrong installer rejected' 'stable installer default does not match' FIXTURE_INSTALLER_VERSION=v1.0.1 PROBE_404_RELEASE_COMMIT="${FIXTURE_HEAD}"
check 'clean exact commit reaches compiler without tag' COMPILE_GATE_REACHED PROBE_404_RELEASE_COMMIT="${FIXTURE_HEAD}"
check 'clean exact existing tag reaches compiler' COMPILE_GATE_REACHED FIXTURE_TAG=1 PROBE_404_RELEASE_COMMIT=
check 'matching tag and explicit commit reaches compiler' COMPILE_GATE_REACHED FIXTURE_TAG=1 PROBE_404_RELEASE_COMMIT="${FIXTURE_HEAD}"
for version in v1.0.8-beta.1 v1.0.8-rc.1; do
  if env PROBE_404_RELEASE_COMMIT="${FIXTURE_HEAD}" bash "${root}/scripts/build-release-assets.sh" "${version}" >"${temporary}/result" 2>&1; then exit 1; fi
  grep -Fq COMPILE_GATE_REACHED "${temporary}/result" || { cat "${temporary}/result"; exit 1; }
  printf '%s: explicit clean commit reaches compiler, no tag created\n' "${version}"
done
for version in v1.0.8-rc.0 v1.0.8-rc.01 v1.0.8-rc.18446744073709551616; do
  if env PROBE_404_RELEASE_COMMIT="${FIXTURE_HEAD}" bash "${root}/scripts/build-release-assets.sh" "${version}" >"${temporary}/result" 2>&1; then exit 1; fi
  if grep -Fq COMPILE_GATE_REACHED "${temporary}/result"; then cat "${temporary}/result"; exit 1; fi
done

# --- Go toolchain and ELF build-info gates -------------------------------------------------------
gate() { # name, expected text, env...
  rm -rf -- "${PROBE_404_RELEASE_OUTPUT}"
  check "$@"
}
gate 'compiler other than the go.mod toolchain is rejected before any build' 'wrong Go compiler' FIXTURE_GO_VERSION=1.26.5 PROBE_404_RELEASE_COMMIT="${FIXTURE_HEAD}"
grep -Fq "reports go1.26.5 but go.mod requires go${FIXTURE_EXPECTED_GO}" "${temporary}/result" || { cat "${temporary}/result"; exit 1; }
grep -Fq 'GOTOOLCHAIN=local' "${temporary}/result" || { cat "${temporary}/result"; exit 1; }
[[ ! -e "${PROBE_404_RELEASE_OUTPUT}" ]] || { printf 'a rejected compiler left an output directory\n' >&2; exit 1; }
gate 'GOTOOLCHAIN=local style older compiler is rejected too' 'wrong Go compiler' FIXTURE_GO_VERSION=1.20.0 GOTOOLCHAIN=local PROBE_404_RELEASE_COMMIT="${FIXTURE_HEAD}"
gate 'ELF built with the wrong Go version is rejected' "was built with go1.26.5, expected go${FIXTURE_EXPECTED_GO}" FIXTURE_ELF_MODE=1 FIXTURE_ELF_GO=1.26.5 PROBE_404_RELEASE_COMMIT="${FIXTURE_HEAD}"
[[ ! -e "${PROBE_404_RELEASE_OUTPUT}" ]] || { printf 'a failed build left its output directory\n' >&2; exit 1; }
gate 'ELF with vcs.modified=true is rejected' 'vcs.modified is not false' FIXTURE_ELF_MODE=1 FIXTURE_ELF_MODIFIED=true PROBE_404_RELEASE_COMMIT="${FIXTURE_HEAD}"
[[ ! -e "${PROBE_404_RELEASE_OUTPUT}" ]] || { printf 'a failed build left its output directory\n' >&2; exit 1; }
gate 'ELF with another vcs.revision is rejected' 'expected VCS revision' FIXTURE_ELF_MODE=1 FIXTURE_ELF_REVISION=ffffffffffffffffffffffffffffffffffffffff PROBE_404_RELEASE_COMMIT="${FIXTURE_HEAD}"
gate 'ELFs with the right compiler, revision and vcs.modified=false pass the build-info gates' METADATA_GATE_REACHED FIXTURE_ELF_MODE=1 PROBE_404_RELEASE_COMMIT="${FIXTURE_HEAD}"
# A pre-existing output directory is not deleted; only what this run wrote is removed.
rm -rf -- "${PROBE_404_RELEASE_OUTPUT}"; mkdir -p "${PROBE_404_RELEASE_OUTPUT}"; printf keep >"${PROBE_404_RELEASE_OUTPUT}/keep.txt"
check 'failed build in an existing directory keeps unrelated files' 'vcs.modified is not false' FIXTURE_ELF_MODE=1 FIXTURE_ELF_MODIFIED=true PROBE_404_RELEASE_COMMIT="${FIXTURE_HEAD}"
[[ -f "${PROBE_404_RELEASE_OUTPUT}/keep.txt" && ! -e "${PROBE_404_RELEASE_OUTPUT}/install.sh" ]] || { ls -la "${PROBE_404_RELEASE_OUTPUT}"; exit 1; }
printf 'Release build gate tests passed (mock compiler; not a release build).\n'
