#!/usr/bin/env bash

set -Eeuo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
temporary="$(mktemp -d)"
trap 'rm -rf -- "${temporary}"' EXIT

real_sha256sum="$(command -v sha256sum)"
mkdir -p "${temporary}/fake-bin"
printf 'rehearsal installer\n' >"${temporary}/install.sh"
digest="$(sha256sum "${temporary}/install.sh" | awk '{print $1}')"

cat >"${temporary}/fake-bin/sha256sum" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
[[ "${FAKE_FAIL:-0}" == 0 ]] || exit 17
[[ $# -eq 0 ]]
cat >/dev/null
printf '%s%s-\n' "${FAKE_DIGEST}" "${FAKE_MARKER}"
EOF
chmod 0700 "${temporary}/fake-bin/sha256sum"

for marker in '  ' ' *'; do
  rm -f -- "${temporary}/SHA256SUMS"
  PATH="${temporary}/fake-bin:${PATH}" FAKE_DIGEST="${digest}" FAKE_MARKER="${marker}" \
    bash "${root}/scripts/write-installer-checksum.sh" \
      "${temporary}/install.sh" "${temporary}/SHA256SUMS"
  grep -Fxq "${digest}  install.sh" "${temporary}/SHA256SUMS"
  [[ "$(grep -c . "${temporary}/SHA256SUMS")" -eq 1 ]]
  (cd "${temporary}" && "${real_sha256sum}" --check --strict SHA256SUMS >/dev/null)
done

printf 'sentinel\n' >"${temporary}/SHA256SUMS"
if PATH="${temporary}/fake-bin:${PATH}" FAKE_FAIL=1 FAKE_DIGEST="${digest}" FAKE_MARKER='  ' \
  bash "${root}/scripts/write-installer-checksum.sh" \
    "${temporary}/install.sh" "${temporary}/SHA256SUMS"; then
  printf 'installer checksum helper masked sha256sum failure\n' >&2
  exit 1
fi
[[ "$(cat "${temporary}/SHA256SUMS")" == sentinel ]]

# GNU escapes a literal backslash in a file operand on Linux.
if [[ "$(uname -s)" == Linux ]]; then
  escaped_directory="${temporary}/path\with-backslash"
  mkdir -- "${escaped_directory}"
  cp -- "${temporary}/install.sh" "${escaped_directory}/install.sh"
  bash "${root}/scripts/write-installer-checksum.sh" \
    "${escaped_directory}/install.sh" "${escaped_directory}/SHA256SUMS"
  (cd "${escaped_directory}" && "${real_sha256sum}" --check --strict SHA256SUMS)
fi

printf 'Release installer manifest format tests passed.\n'
