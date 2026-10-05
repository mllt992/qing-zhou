#!/usr/bin/env bash
# Apply exactly the frozen Trojan handshake maintenance patch (qing-zhou #87).
# This is a project maintenance patch, not an upstream sing-box fix. It touches
# only transport/trojan/service.go and runs after the transport buffer backport.
set -euo pipefail
root=$(cd "$(dirname "$0")" && pwd)
source "$root/singbox-pins.sh"
(($# == 1)) || { echo "Usage: $0 SOURCE_DIR" >&2; exit 2; }
source_dir=$(cd "$1" && pwd)
patch="$root/trojan-handshake/qz-trojan-handshake.patch"
manifest="$root/trojan-handshake/manifest.json"
[[ $(git -C "$source_dir" rev-parse HEAD) == "$SB_COMMIT" ]] || { echo 'Unreviewed sing-box base' >&2; exit 1; }
[[ $(sha256sum "$patch" | cut -d' ' -f1) == "$TROJAN_PATCH_SHA256" ]] || { echo 'Trojan patch hash mismatch' >&2; exit 1; }
python3 - "$manifest" "$source_dir" "$SB_COMMIT" "$TROJAN_PATCH_SHA256" before_sha256 <<'PY'
import hashlib,json,pathlib,sys
m=json.load(open(sys.argv[1]));root=pathlib.Path(sys.argv[2])
assert m['base_commit']==sys.argv[3] and m['patch_sha256']==sys.argv[4]
assert m['upstream_fix'] is False and m['kind']=='qing-zhou maintenance patch'
assert {f['path'] for f in m['files']}=={'transport/trojan/service.go'}
for f in m['files']: assert hashlib.sha256((root/f['path']).read_bytes()).hexdigest()==f[sys.argv[5]], 'unexpected source content: '+f['path']
PY
git -C "$source_dir" apply --check "$patch"
git -C "$source_dir" apply "$patch"
python3 - "$manifest" "$source_dir" <<'PY'
import hashlib,json,pathlib,sys
m=json.load(open(sys.argv[1]));root=pathlib.Path(sys.argv[2])
for f in m['files']: assert hashlib.sha256((root/f['path']).read_bytes()).hexdigest()==f['after_sha256'], 'unexpected patched content: '+f['path']
print('Verified qing-zhou Trojan handshake maintenance patch sha256='+m['patch_sha256'])
PY
