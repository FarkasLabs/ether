#!/bin/sh
# Guard the container runtime against a libva older than the pinned
# Jellyfin FFmpeg. Bookworm's libva 2.17 has no vaMapBuffer2; trixie's
# libva 2.22 does. This checks the Dockerfiles only — it does not build
# an image or prove a GPU.
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
fail=0

require() {
	file=$1
	needle=$2
	if ! grep -q -F -- "$needle" "$file"; then
		echo "missing [$needle] in $file" >&2
		fail=1
	fi
}

forbid() {
	file=$1
	needle=$2
	if grep -q -F -- "$needle" "$file"; then
		echo "unexpected [$needle] in $file" >&2
		fail=1
	fi
}

root_df=$root/Dockerfile
amd64_df=$root/packaging/docker/Dockerfile.goreleaser.amd64
arm64_df=$root/packaging/docker/Dockerfile.goreleaser.arm64

for file in "$root_df" "$amd64_df" "$arm64_df"; do
	forbid "$file" 'debian:bookworm'
	require "$file" 'debian:trixie-slim'
	require "$file" 'libva2'
	require "$file" 'libva-drm2'
	require "$file" 'mesa-va-drivers'
	require "$file" 'ocl-icd-libopencl1'
	require "$file" '--gid 999'
	require "$file" '--uid 999'
	forbid "$file" 'libmfx1'
done

require "$root_df" 'TARGETARCH'
require "$root_df" '= amd64'
require "$root_df" 'intel-media-va-driver'
require "$root_df" 'i965-va-driver'
require "$root_df" 'libmfx-gen1.2'

require "$amd64_df" 'intel-media-va-driver'
require "$amd64_df" 'i965-va-driver'
require "$amd64_df" 'libmfx-gen1.2'

forbid "$arm64_df" 'intel-media-va-driver'
forbid "$arm64_df" 'i965-va-driver'
forbid "$arm64_df" 'libmfx-gen1.2'

if [ "$fail" -ne 0 ]; then
	exit 1
fi
echo "docker runtime stack: trixie libva, uid 999"
