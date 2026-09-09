#!/usr/bin/env bash

set -Eeuo pipefail

ROOT_DIRECTORY="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly ROOT_DIRECTORY
readonly VERSION="${1:-v0.8.0}"
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
[[ "${VERSION}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || {
  printf 'version must look like v0.7.0\n' >&2
  exit 1
}

cd "${ROOT_DIRECTORY}"
[[ -z "$(git status --porcelain --untracked-files=all)" ]] || {
  printf 'release builds require a clean git worktree\n' >&2
  exit 1
}
git show-ref --verify --quiet "refs/tags/${VERSION}" || {
  printf 'release tag %s does not exist\n' "${VERSION}" >&2
  exit 1
}
head_commit="$(git rev-parse --verify HEAD)"
tag_commit="$(git rev-parse --verify "${VERSION}^{commit}")"
[[ "${head_commit}" == "${tag_commit}" ]] || {
  printf 'HEAD %s does not match tag %s commit %s\n' "${head_commit}" "${VERSION}" "${tag_commit}" >&2
  exit 1
}

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

asset_paths=()
for architecture in amd64 arm64; do
  for role in server agent; do
    asset="404-probe-${role}-linux-${architecture}"
    package="./cmd/${role}"
    printf 'Building %s\n' "${asset}"
    CGO_ENABLED=0 GOOS=linux GOARCH="${architecture}" \
      go build -trimpath -ldflags="-s -w -X 404-probe/internal/buildinfo.Version=${VERSION} -X 404-probe/internal/buildinfo.Commit=${head_commit}" -o "${OUTPUT_DIRECTORY}/${asset}" "${package}"
    metadata="$(go version -m "${OUTPUT_DIRECTORY}/${asset}")"
    grep -Fq "vcs.revision=${head_commit}" <<<"${metadata}" || {
      printf '%s does not contain the expected VCS revision\n' "${asset}" >&2
      exit 1
    }
    if grep -Fq 'vcs.modified=true' <<<"${metadata}"; then
      printf '%s was built from a modified worktree\n' "${asset}" >&2
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
