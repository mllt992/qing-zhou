#!/usr/bin/env bash
# Deterministic library regressions only; full real-core traffic runs separately.
#
# Official 1.14.3+ already contains the selected 07512b10 WS/HTTPUpgrade buffer
# fix, so the current pin is tested without re-applying that backport. The
# --check-baseline negative controls still run against the frozen 1.14.2 tree
# where the bug existed.
set -euo pipefail
root=$(cd "$(dirname "$0")" && pwd)
source "$root/singbox-pins.sh"
GO=${GO:-go}
export GOWORK=off GOTOOLCHAIN=local
check_baseline=false
source_dir=
rounds=${QZ_TRANSPORT_STRESS_ROUNDS:-20}
while (($#)); do
  case "$1" in
    --check-baseline) check_baseline=true; shift ;;
    --source-dir) source_dir=${2:?Missing source directory}; shift 2 ;;
    *) echo "Usage: $0 [--check-baseline] [--source-dir LOCAL_GIT_REPO]" >&2; exit 2 ;;
  esac
done
# The frozen Trojan maintenance patch (#87) is regressed by its own script; it
# is chained here so every CI/Release invocation of this step also runs it.
trojan_args=()
"$check_baseline" && trojan_args+=(--check-baseline)
[[ -n "$source_dir" ]] && trojan_args+=(--source-dir "$(cd "$source_dir" && pwd)")
[[ "$rounds" =~ ^[1-9][0-9]*$ && "$rounds" -le 1000 ]] || { echo 'Invalid stress rounds' >&2; exit 2; }
[[ $("$GO" env GOVERSION) == go1.25.14 ]] || { echo 'Use pinned Go 1.25.14' >&2; exit 1; }
work=$(mktemp -d "${TMPDIR:-/tmp}/qz-transport-regression.XXXXXXXX")
trap 'rm -rf -- "$work"' EXIT

checkout_pin() {
  local dest=$1 tag=$2 commit=$3
  if [[ -n "$source_dir" ]] && git -C "$(cd "$source_dir" && pwd)" cat-file -e "${commit}^{commit}" 2>/dev/null; then
    git clone --quiet --no-hardlinks --no-checkout "$(cd "$source_dir" && pwd)" "$dest"
    git -C "$dest" checkout --quiet --detach "$commit"
  else
    git clone --quiet --depth 1 --branch "$tag" https://github.com/SagerNet/sing-box.git "$dest"
  fi
  [[ $(git -C "$dest" rev-parse HEAD) == "$commit" && -z $(git -C "$dest" status --porcelain) ]] || { echo "Unreviewed core base for $tag" >&2; exit 1; }
}

packages=(./transport/v2rayhttpupgrade ./transport/v2raywebsocket)
if "$check_baseline"; then
  checkout_pin "$work/baseline" "$TRANSPORT_BASELINE_TAG" "$TRANSPORT_BASELINE_COMMIT"
  cp "$root/transport-buffer/httpupgrade_test.go.txt" "$work/baseline/transport/v2rayhttpupgrade/qz_buffer_regression_test.go"
  cp "$root/transport-buffer/websocket_test.go.txt" "$work/baseline/transport/v2raywebsocket/qz_buffer_regression_test.go"
  if (cd "$work/baseline" && "$GO" test -mod=readonly -run '^TestQZ(HTTPUpgradeClientPreservesCoalescedFirstPayload|HTTPUpgradeServerPreservesHijackReadAhead|WebSocketClientPreservesCoalescedFirstFrame)$' -count=1 -json "${packages[@]}" > "$work/baseline.json"); then
    echo 'Unpatched 1.14.2 base unexpectedly passed negative controls' >&2; exit 1
  fi
  python3 - "$work/baseline.json" <<'PY'
import json,sys
failed={e.get('Test') for line in open(sys.argv[1]) if (e:=json.loads(line)).get('Action')=='fail'}
expected={'TestQZHTTPUpgradeClientPreservesCoalescedFirstPayload','TestQZWebSocketClientPreservesCoalescedFirstFrame'}|{'TestQZHTTPUpgradeServerPreservesHijackReadAhead/'+str(n) for n in (1,4,11)}
assert expected<=failed, 'baseline did not run and fail all five expected buffer-loss cases'
print('Unpatched frozen 1.14.2: all five exact coalesced/read-ahead negative controls reproduced')
PY
fi

checkout_pin "$work/source" "$SB_TAG" "$SB_COMMIT"
cd "$work/source"
"$GO" mod download -json "github.com/sagernet/sing-vmess@$VMESS_VERSION" > "$work/vmess.json"
python3 - "$work/vmess.json" "$VMESS_VERSION" "$VMESS_SUM" "$VMESS_MOD_SUM" "$VMESS_COMMIT" <<'PY'
import json,sys
v=json.load(open(sys.argv[1]))
assert v['Version']==sys.argv[2] and v['Sum']==sys.argv[3] and v['GoModSum']==sys.argv[4]
assert not v.get('Origin') or v['Origin']['Hash']==sys.argv[5]
PY
# Official 1.14.5 already has the buffer fix; do not apply the 1.14.2 backport.
cp "$root/transport-buffer/httpupgrade_test.go.txt" transport/v2rayhttpupgrade/qz_buffer_regression_test.go
cp "$root/transport-buffer/websocket_test.go.txt" transport/v2raywebsocket/qz_buffer_regression_test.go
if ! "$GO" test -mod=readonly -race -timeout=5m -run '^TestQZ' -count="$rounds" -json "${packages[@]}" > "$work/fixed.json"; then
  tail -200 "$work/fixed.json" >&2
  exit 1
fi
python3 - "$work/fixed.json" "$rounds" <<'PY'
import collections,json,sys
passed=collections.Counter();ran=collections.Counter();skipped=[]
for line in open(sys.argv[1]):
 e=json.loads(line);name=e.get('Test')
 if name and e.get('Action')=='pass':passed[name]+=1
 if name and e.get('Action')=='run':ran[name]+=1
 if e.get('Action')=='skip':skipped.append(name)
expected={'TestQZHTTPUpgradeClientPreservesCoalescedFirstPayload','TestQZHTTPUpgradeServerPreservesHijackReadAhead','TestQZHTTPUpgradeFragmentedAndLongFlow','TestQZHTTPUpgradeConcurrentIndependentBuffers','TestQZWebSocketClientPreservesCoalescedFirstFrame','TestQZWebSocketConcurrentSharedHeaders','TestQZWebSocketFragmentedAndLongFlow'}
rounds=int(sys.argv[2]);assert not skipped and expected<=passed.keys() and all(passed[n]==rounds for n in expected)
assert ran==passed, 'a started regression did not pass'
print('Official '+str(rounds)+' race rounds for each of '+str(len(expected))+' required groups on the pinned 1.14.5 tree; no skips or traffic retries')
for n in sorted(expected):print('PASS '+n+' x'+str(passed[n]))
print('Covers 1/7-byte fragmentation, coalesced first data, no payload/normal close, up to 1 MiB flows, 32 concurrent connections and shared WS subprotocol headers')
PY
cd "$root/.."
bash "$root/test-trojan-handshake.sh" "${trojan_args[@]}"
