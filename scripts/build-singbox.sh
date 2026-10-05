#!/usr/bin/env bash
# Build the metering-enabled core from one reviewed upstream source pair plus
# the frozen transport backport and Trojan maintenance patch.
# No nodes are contacted. --source-dir reads a local Git repository's pinned
# commit only; its worktree is never edited and uncommitted changes are ignored.
set -euo pipefail

script_root=$(cd "$(dirname "$0")" && pwd)
source "$script_root/singbox-pins.sh"
TAGS=with_gvisor,with_quic,with_grpc,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api,with_v2ray_api,with_tailscale,with_ccm,with_ocm,with_cloudflared,with_naive_outbound,with_usbip,with_openvpn,with_openconnect,with_purego,badlinkname,tfogo_checklinkname0
GO=${GO:-go}
export GOWORK=off GOTOOLCHAIN=local
output_dir=
architectures=amd64,arm64
source_dir=
usage() { echo "Usage: $0 --output-dir DIR [--arch amd64,arm64] [--source-dir LOCAL_GIT_REPO]"; }
while (($#)); do
  case "$1" in
    --output-dir|--arch|--source-dir)
      (($# >= 2)) || { usage >&2; exit 2; }
      case "$1" in
        --output-dir) output_dir=$2 ;;
        --arch) architectures=$2 ;;
        --source-dir) source_dir=$2 ;;
      esac
      shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
done
[[ -n "$output_dir" ]] || { usage >&2; exit 2; }
[[ "$architectures" =~ ^(amd64|arm64)(,(amd64|arm64))*$ ]] || { echo 'Only linux/amd64 and linux/arm64 are supported' >&2; exit 2; }
for command in "$GO" git python3 sha256sum; do command -v "$command" >/dev/null || { echo "Missing command: $command" >&2; exit 1; }; done
[[ $("$GO" env GOVERSION) == go1.25.14 ]] || { echo 'Use the pinned Go 1.25.14 toolchain' >&2; exit 1; }
mkdir -p "$output_dir"
output_dir=$(cd "$output_dir" && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/qz-singbox-build.XXXXXXXX")
trap 'rm -rf -- "$work"' EXIT
if [[ -n "$source_dir" ]]; then
  git clone --quiet --no-hardlinks --no-checkout "$(cd "$source_dir" && pwd)" "$work/source"
  git -C "$work/source" checkout --quiet --detach "$SB_COMMIT"
else
  git clone --quiet --depth 1 --branch "$SB_TAG" https://github.com/SagerNet/sing-box.git "$work/source"
fi
cd "$work/source"
[[ $(git rev-parse HEAD) == "$SB_COMMIT" ]] || { echo 'Unreviewed sing-box base commit' >&2; exit 1; }
[[ -z $(git status --porcelain) ]] || { echo 'Base checkout is not clean' >&2; exit 1; }
cp go.mod "$work/base.go.mod"
# Avoid `go get`: it can select unrelated dependency upgrades. A readonly build
# below must accept exactly this one require-line change.
"$GO" mod edit -require="github.com/sagernet/sing-vmess@$VMESS_VERSION"
python3 - "$work/base.go.mod" go.mod "$VMESS_VERSION" <<'PY'
import pathlib, sys
before, after = (pathlib.Path(p).read_text() for p in sys.argv[1:3])
old = 'github.com/sagernet/sing-vmess v0.2.8'
assert before.count(old) == 1, 'unexpected base sing-vmess requirement'
assert after == before.replace(old, 'github.com/sagernet/sing-vmess ' + sys.argv[3]), 'unrelated go.mod change'
PY
"$GO" mod download -json "github.com/sagernet/sing-vmess@$VMESS_VERSION" > "$work/vmess.json"
python3 - "$work/vmess.json" "$VMESS_VERSION" "$VMESS_COMMIT" "$VMESS_SUM" "$VMESS_MOD_SUM" <<'PY'
import json, sys
info = json.load(open(sys.argv[1]))
assert info.get('Path') == 'github.com/sagernet/sing-vmess'
assert info.get('Version') == sys.argv[2]
assert info.get('Sum') == sys.argv[4], 'sing-vmess module sum mismatch'
assert info.get('GoModSum') == sys.argv[5], 'sing-vmess go.mod sum mismatch'
origin = info.get('Origin')
# Older Go proxies can omit Origin; the full module and go.mod hashes remain
# mandatory. When origin evidence exists, never accept a different commit.
if origin:
    assert origin.get('Hash') == sys.argv[3], 'sing-vmess origin commit mismatch'
PY
bash "$script_root/apply-transport-buffer-patch.sh" "$PWD"
bash "$script_root/apply-trojan-handshake-patch.sh" "$PWD"
cp go.mod "$work/expected.go.mod"
printf 'Verified sing-box %s (%s), sing-vmess %s (%s)\n' "$SB_TAG" "$SB_COMMIT" "$VMESS_VERSION" "$VMESS_SUM"
IFS=, read -r -a arches <<< "$architectures"
for arch in "${arches[@]}"; do
  binary="$output_dir/sing-box-linux-$arch"
  echo "Building $CORE_VERSION linux/$arch"
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 "$GO" build -mod=readonly -trimpath \
    -tags "$TAGS" \
    -ldflags "-s -w -checklinkname=0 -X github.com/sagernet/sing-box/constant.Version=$CORE_VERSION" \
    -o "$binary" ./cmd/sing-box
  cmp go.mod "$work/expected.go.mod"
  "$GO" version -m "$binary" > "$work/buildinfo-$arch.txt"
  python3 - "$work/buildinfo-$arch.txt" "$VMESS_VERSION" "$VMESS_SUM" "$SB_COMMIT" "$arch" <<'PY'
import pathlib, sys
text = pathlib.Path(sys.argv[1]).read_text()
assert '\tdep\tgithub.com/sagernet/sing-vmess\t' + sys.argv[2] + '\t' + sys.argv[3] in text
assert '\tbuild\tvcs.revision=' + sys.argv[4] in text
assert '\tbuild\tGOOS=linux' in text and '\tbuild\tGOARCH=' + sys.argv[5] in text
assert 'with_v2ray_api' in text
PY
  if [[ $("$GO" env GOHOSTOS) == linux && $("$GO" env GOHOSTARCH) == "$arch" ]]; then
    "$binary" version > "$work/version-$arch.txt"
    grep -Fx "sing-box version $CORE_VERSION" "$work/version-$arch.txt" >/dev/null
    grep -q with_v2ray_api "$work/version-$arch.txt"
    head -n 1 "$work/version-$arch.txt"
  fi
  (cd "$output_dir" && sha256sum "sing-box-linux-$arch")
done
python3 - "$output_dir" "$SB_TAG" "$SB_COMMIT" "$VMESS_VERSION" "$VMESS_COMMIT" "$VMESS_SUM" "$VMESS_MOD_SUM" "$CORE_VERSION" "$architectures" "$TAGS" "$work" "$script_root/transport-buffer/manifest.json" "$script_root/trojan-handshake/manifest.json" <<'PY'
import hashlib, json, pathlib, sys
directory = pathlib.Path(sys.argv[1])
work = pathlib.Path(sys.argv[11])
data = {
    'schema_version': 3,
    'sing_box_tag': sys.argv[2],
    'sing_box_commit': sys.argv[3],
    'sing_vmess_version': sys.argv[4],
    'sing_vmess_commit': sys.argv[5],
    'sing_vmess_module_sum': sys.argv[6],
    'sing_vmess_go_mod_sum': sys.argv[7],
    'core_version': sys.argv[8],
    'transport_buffer_patch': json.load(open(sys.argv[12])),
    # Project maintenance patch (qing-zhou #87), not an upstream fix.
    'trojan_handshake_patch': json.load(open(sys.argv[13])),
    'go_version': 'go1.25.14',
    'build_tags': sys.argv[10].split(','),
    'binaries': [],
}
for arch in dict.fromkeys(sys.argv[9].split(',')):
    name = 'sing-box-linux-' + arch
    info = (work / ('buildinfo-' + arch + '.txt')).read_text()
    version_path = work / ('version-' + arch + '.txt')
    # Keep the output directory release-ready: exactly requested binaries and
    # this JSON. Source/toolchain identity was checked against actual buildinfo.
    data['binaries'].append({'os': 'linux', 'arch': arch, 'filename': name,
        'sha256': hashlib.file_digest(open(directory / name, 'rb'), 'sha256').hexdigest(),
        'build_go_version': info.splitlines()[0].rsplit(': ', 1)[1],
        'build_vcs_revision': next(line.strip().removeprefix('build\tvcs.revision=') for line in info.splitlines() if '\tbuild\tvcs.revision=' in line),
        'build_vmess_dependency': next(line.strip().removeprefix('dep\t') for line in info.splitlines() if '\tdep\tgithub.com/sagernet/sing-vmess\t' in line),
        'version_output': version_path.read_text() if version_path.exists() else None})
(directory / 'sing-box-provenance.json').write_text(json.dumps(data, indent=2) + '\n')
print('Wrote sing-box-provenance.json for ' + data['core_version'])
PY
