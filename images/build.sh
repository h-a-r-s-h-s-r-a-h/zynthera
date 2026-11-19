#!/usr/bin/env bash
# Builds one golden image and prints its path and version.
#
#   images/build.sh <slug> [version]
#
#   slug      for example ubuntu-24.04-xfce (a template images/packer/<slug>.pkr.hcl must exist)
#   version   date-based, for example 2026.11.1 (default: <year>.<month>.1)
#
# Needs KVM, Packer with the QEMU plugin, qemu-img, xorriso and Go.
# The result goes to $ZYN_IMAGES_DIR/<slug>/<version>.qcow2
# (default /var/lib/zynthera/images). An existing image is never overwritten:
# VMs cloned from it read from it for their whole life.
set -euo pipefail
cd "$(dirname "$0")/.."

slug="${1:?usage: images/build.sh <slug> [version]}"
version="${2:-$(date +%Y.%m).1}"
images_dir="${ZYN_IMAGES_DIR:-/var/lib/zynthera/images}"
template="images/packer/${slug}.pkr.hcl"
target="${images_dir}/${slug}/${version}.qcow2"

[[ "$slug" =~ ^[a-z0-9][a-z0-9.-]*$ ]] || { echo "bad slug: $slug" >&2; exit 1; }
[[ "$version" =~ ^[0-9][0-9a-z.-]*$ ]] || { echo "bad version: $version" >&2; exit 1; }
[[ -f "$template" ]] || { echo "no template $template" >&2; exit 1; }
[[ -e /dev/kvm && -r /dev/kvm && -w /dev/kvm ]] || { echo "KVM is not usable (/dev/kvm)" >&2; exit 1; }
if [[ -e "$target" ]]; then
  echo "refusing to overwrite $target: build a new version instead" >&2
  exit 1
fi
for tool in packer qemu-img xorriso; do
  command -v "$tool" >/dev/null || { echo "missing tool: $tool" >&2; exit 1; }
done

work="$(mktemp -d "${TMPDIR:-/var/tmp}/zyn-image-build.XXXXXX")"
trap 'rm -rf "$work"' EXIT

# The file bridge is baked into the image. A server without Go passes an
# already built binary in ZYN_BRIDGE_BINARY (scripts/install.sh does).
if [[ -n "${ZYN_BRIDGE_BINARY:-}" ]]; then
  [[ -x "$ZYN_BRIDGE_BINARY" ]] || { echo "ZYN_BRIDGE_BINARY is not an executable file: $ZYN_BRIDGE_BINARY" >&2; exit 1; }
  cp "$ZYN_BRIDGE_BINARY" "$work/zynthera-file-bridge"
else
  command -v go >/dev/null || { echo "missing tool: go (or set ZYN_BRIDGE_BINARY)" >&2; exit 1; }
  echo "==> building the file bridge"
  CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$work/zynthera-file-bridge" ./cmd/file-bridge
fi

echo "==> building $slug $version (this takes 10 to 20 minutes)"
packer init "$template" >/dev/null
packer build -color=false \
  -var "output_dir=$work/out" \
  -var "bridge_binary=$work/zynthera-file-bridge" \
  "$template"

echo "==> compressing"
mkdir -p "$(dirname "$target")"
qemu-img convert -c -O qcow2 "$work/out/build.qcow2" "$target.partial"
# Readable by the user QEMU runs as.
chmod 0644 "$target.partial"
mv "$target.partial" "$target"

echo "==> done"
qemu-img info "$target" | sed -n '1,6p'
echo "image:   $target"
echo "version: $version"
