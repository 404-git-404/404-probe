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
  show) printf '#!/usr/bin/env bash\nreadonly DEFAULT_VERSION="%s"\n' "${FIXTURE_INSTALLER_VERSION:-v1.0.2}" ;;
  *) exit 69 ;;
esac
SH
cat >"${temporary}/bin/go" <<'SH'
#!/usr/bin/env bash
printf '%s\n' 'COMPILE_GATE_REACHED'
exit 71
SH
chmod 0700 "${temporary}/bin/git" "${temporary}/bin/go"
export PATH="${temporary}/bin:${PATH}"
export FIXTURE_HEAD=0123456789abcdef0123456789abcdef01234567
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
check 'default still requires tag' 'release tag v1.0.2 does not exist' PROBE_404_RELEASE_COMMIT=
check 'short commit rejected' 'full clean HEAD commit' PROBE_404_RELEASE_COMMIT=0123456
check 'wrong commit rejected' 'full clean HEAD commit' PROBE_404_RELEASE_COMMIT=ffffffffffffffffffffffffffffffffffffffff
check 'conflicting existing tag rejected' 'existing release tag does not match' FIXTURE_TAG=1 FIXTURE_TAG_COMMIT=ffffffffffffffffffffffffffffffffffffffff PROBE_404_RELEASE_COMMIT="${FIXTURE_HEAD}"
check 'wrong installer rejected' 'stable installer default does not match' FIXTURE_INSTALLER_VERSION=v1.0.1 PROBE_404_RELEASE_COMMIT="${FIXTURE_HEAD}"
check 'clean exact commit reaches compiler without tag' COMPILE_GATE_REACHED PROBE_404_RELEASE_COMMIT="${FIXTURE_HEAD}"
check 'clean exact existing tag reaches compiler' COMPILE_GATE_REACHED FIXTURE_TAG=1 PROBE_404_RELEASE_COMMIT=
check 'matching tag and explicit commit reaches compiler' COMPILE_GATE_REACHED FIXTURE_TAG=1 PROBE_404_RELEASE_COMMIT="${FIXTURE_HEAD}"
printf 'Release build gate tests passed (mock compiler; not a release build).\n'
