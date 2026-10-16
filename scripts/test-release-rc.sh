#!/usr/bin/env bash
# Evaluate only the actual pure version functions, never the installer main.
set -Eeuo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
eval "$(awk '/^(release_version|canonical_version|compare_versions|compare_release_numbers)\(\) \{/{copy=1} copy{print} copy && /^\}/{copy=0}' "${root}/install.sh")"
chain=(v1.0.2 v1.0.3-beta.1 v1.0.3-beta.18446744073709551615 v1.0.3-rc.1 v1.0.3-rc.2 v1.0.3-rc.10 v1.0.3)
for ((i=0;i<${#chain[@]};i++)); do
  release_version "${chain[i]}"
  for ((j=0;j<${#chain[@]};j++)); do
    want=0; (( i<j )) && want=-1; (( i>j )) && want=1
    got="$(compare_versions "${chain[i]}" "${chain[j]}")"
    [[ "${got}" == "${want}" ]] || { printf 'ordering mismatch %s %s: %s\n' "${chain[i]}" "${chain[j]}" "${got}"; exit 1; }
  done
done
for invalid in v1.0.3-rc.0 v1.0.3-rc.01 v1.0.3-rc.18446744073709551616 v18446744073709551616.0.0 v1.0.3-RC.1 v1.0.3-rc.1+meta; do
  if release_version "${invalid}"; then printf 'invalid accepted %s\n' "${invalid}"; exit 1; fi
done
if canonical_version v1.0.3-rc.1; then printf 'RC entered canonical Stable\n'; exit 1; fi
printf 'Installer RC: 49 order pairs, invalid/overflow and Stable-isolation passed.\n'
