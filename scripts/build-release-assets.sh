#!/usr/bin/env bash

set -Eeuo pipefail

ROOT_DIRECTORY="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly ROOT_DIRECTORY
readonly VERSION="${1:-v1.0.3}"
readonly RELEASE_COMMIT="${PROBE_404_RELEASE_COMMIT:-}"
readonly OUTPUT_DIRECTORY="${PROBE_404_RELEASE_OUTPUT:-${ROOT_DIRECTORY}/build/release-${VERSION}}"

command -v go >/dev/null 2>&1 || {
  printf 'go is required\n' >&2
  exit 1
}
command -v sha256sum >/dev/null 2>&1 || {
  printf 'sha256sum is required\n' >&2
  exit 1
}
command -v git >/dev/null 2>&1 || {
  printf 'git is required\n' >&2
  exit 1
}
[[ ${#VERSION} -le 64 && "${VERSION}" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(beta|rc)\.[1-9][0-9]*)?$ ]] || {
  printf 'version must be canonical stable or explicit vX.Y.Z-beta.N/rc.N\n' >&2
  exit 1
}
version_numbers="${VERSION#v}"; version_numbers="${version_numbers/-beta./.}"; version_numbers="${version_numbers/-rc./.}"
IFS=. read -r -a components <<<"${version_numbers}"
for component in "${components[@]}"; do
  [[ ${#component} -lt 20 || ( ${#component} -eq 20 && ! "${component}" > 18446744073709551615 ) ]] || { printf 'release version integer overflow\n' >&2; exit 1; }
done

cd "${ROOT_DIRECTORY}"
[[ -z "$(git status --porcelain --untracked-files=all)" ]] || {
  printf 'release builds require a clean git worktree\n' >&2
  exit 1
}
head_commit="$(git rev-parse --verify HEAD)"
if [[ -n "${RELEASE_COMMIT}" ]]; then
  # Pre-publication High review builds the exact clean commit before creating
  # the public tag. This mode never creates or moves any Git reference.
  [[ "${RELEASE_COMMIT}" =~ ^[0-9a-f]{40}$ && "${RELEASE_COMMIT}" == "${head_commit}" ]] || {
    printf 'explicit release commit must be the full clean HEAD commit\n' >&2
    exit 1
  }
  tag_commit="${RELEASE_COMMIT}"
  if git show-ref --verify --quiet "refs/tags/${VERSION}"; then
    [[ "$(git rev-parse --verify "${VERSION}^{commit}")" == "${tag_commit}" ]] || {
      printf 'existing release tag does not match explicit commit\n' >&2
      exit 1
    }
  fi
  printf 'Building unpublished review assets for exact commit %s\n' "${tag_commit}"
else
  git show-ref --verify --quiet "refs/tags/${VERSION}" || {
    printf 'release tag %s does not exist\n' "${VERSION}" >&2
    exit 1
  }
  tag_commit="$(git rev-parse --verify "${VERSION}^{commit}")"
  [[ "${head_commit}" == "${tag_commit}" ]] || {
    printf 'HEAD %s does not match tag %s commit %s\n' "${head_commit}" "${VERSION}" "${tag_commit}" >&2
    exit 1
  }
fi

mkdir -p "${OUTPUT_DIRECTORY}"

installer_path="${OUTPUT_DIRECTORY}/install.sh"
git show "${tag_commit}:install.sh" >"${installer_path}"
[[ -s "${installer_path}" ]] || {
  printf 'tagged installer is empty\n' >&2
  exit 1
}
if LC_ALL=C grep -q $'\r' "${installer_path}"; then
  printf 'tagged installer contains carriage returns\n' >&2
  exit 1
fi
bash -n "${installer_path}"
if [[ "${VERSION}" != *-* ]]; then
  grep -Fxq "readonly DEFAULT_VERSION=\"${VERSION}\"" "${installer_path}" || {
    printf 'stable installer default does not match release version\n' >&2
    exit 1
  }
fi

asset_paths=()
for architecture in amd64 arm64; do
  for role in server agent; do
    asset="404-probe-${role}-linux-${architecture}"
    package="./cmd/${role}"
    printf 'Building %s\n' "${asset}"
    CGO_ENABLED=0 GOOS=linux GOARCH="${architecture}" \
      go build -trimpath -ldflags="-w -X 404-probe/internal/buildinfo.Version=${VERSION} -X 404-probe/internal/buildinfo.Commit=${head_commit}" -o "${OUTPUT_DIRECTORY}/${asset}" "${package}"
    metadata="$(go version -m "${OUTPUT_DIRECTORY}/${asset}")"
    grep -Fq "vcs.revision=${head_commit}" <<<"${metadata}" || {
      printf '%s does not contain the expected VCS revision\n' "${asset}" >&2
      exit 1
    }
    if ! grep -Fq 'vcs.modified=false' <<<"${metadata}"; then
      printf '%s does not prove an unmodified worktree\n' "${asset}" >&2
      exit 1
    fi
    asset_paths+=("${OUTPUT_DIRECTORY}/${asset}")
  done
done

go run ./cmd/release-metadata --version "${VERSION}" --commit "${head_commit}" \
  --output "${OUTPUT_DIRECTORY}" "${asset_paths[@]}"
bash "${ROOT_DIRECTORY}/scripts/write-installer-checksum.sh" \
  "${installer_path}" "${OUTPUT_DIRECTORY}/SHA256SUMS"

(
  cd "${OUTPUT_DIRECTORY}"
  sha256sum --check --strict SHA256SUMS
)

printf '\nRelease assets are ready in %s\n' "${OUTPUT_DIRECTORY}"
printf 'No release was created or modified.\n'
