#!/usr/bin/env bash
# Deterministic library regressions for the Trojan handshake maintenance patch
# (qing-zhou #87). Full real-core relay traffic runs separately in CI.
# Also invoked at the end of test-transport-buffer.sh (CI and Release).
set -euo pipefail
root=$(cd "$(dirname "$0")" && pwd)
source "$root/singbox-pins.sh"
GO=${GO:-go}
export GOWORK=off GOTOOLCHAIN=local
check_baseline=false
source_dir=
rounds=${QZ_TROJAN_STRESS_ROUNDS:-20}
while (($#)); do
  case "$1" in
    --check-baseline) check_baseline=true; shift ;;
    --source-dir) source_dir=${2:?Missing source directory}; shift 2 ;;
    *) echo "Usage: $0 [--check-baseline] [--source-dir LOCAL_GIT_REPO]" >&2; exit 2 ;;
  esac
done
[[ "$rounds" =~ ^[1-9][0-9]*$ && "$rounds" -le 200 ]] || { echo 'Invalid stress rounds' >&2; exit 2; }
[[ $("$GO" env GOVERSION) == go1.25.14 ]] || { echo 'Use pinned Go 1.25.14' >&2; exit 1; }
work=$(mktemp -d "${TMPDIR:-/tmp}/qz-trojan-regression.XXXXXXXX")
trap 'rm -rf -- "$work"' EXIT
if [[ -n "$source_dir" ]]; then
  git clone --quiet --no-hardlinks --no-checkout "$(cd "$source_dir" && pwd)" "$work/source"
  git -C "$work/source" checkout --quiet --detach "$SB_COMMIT"
else
  git clone --quiet --depth 1 --branch "$SB_TAG" https://github.com/SagerNet/sing-box.git "$work/source"
fi
cd "$work/source"
[[ $(git rev-parse HEAD) == "$SB_COMMIT" && -z $(git status --porcelain) ]] || { echo 'Unreviewed core base' >&2; exit 1; }
tests="$root/trojan-handshake"
package=./transport/trojan
tags=with_grpc,with_quic
if "$check_baseline"; then
  # Only the public-API file: it compiles on the unpatched base, so a failure
  # here is a real behavioural negative control, not a build error.
  cp "$tests/fragment_test.go.txt" transport/trojan/qz_trojan_fragment_test.go
  if "$GO" test -mod=readonly -tags "$tags" -run '^TestQZTrojan' -count=1 -json "$package" > "$work/baseline.json"; then
    echo 'Unpatched base unexpectedly passed the Trojan fragment negative controls' >&2; exit 1
  fi
  python3 - "$work/baseline.json" <<'PY'
import json,sys
failed=set();passed=set()
for line in open(sys.argv[1]):
    e=json.loads(line);t=e.get('Test')
    if not t: continue
    if e.get('Action')=='fail': failed.add(t)
    if e.get('Action')=='pass': passed.add(t)
neg=set()
for split in ('bytewise','key1+rest','key10+rest','key55+rest','7byte'):
    for cmd in (1,3):
        for fb in ('false','true'):
            neg.add('TestQZTrojanFragmentedRequest/%s/cmd-%d/fallback-%s'%(split,cmd,fb))
for cache in (1,4,11,55):
    for cmd in (1,3):
        for fb in ('false','true'):
            neg.add('TestQZTrojanReadAheadCache/cache-%d/cmd-%d/fallback-%s'%(cache,cmd,fb))
pos=set()
for split in ('whole','key56+rest','key57+rest'):
    for cmd in (1,3):
        for fb in ('false','true'):
            pos.add('TestQZTrojanFragmentedRequest/%s/cmd-%d/fallback-%s'%(split,cmd,fb))
for cache in (56,60):
    for cmd in (1,3):
        for fb in ('false','true'):
            pos.add('TestQZTrojanReadAheadCache/cache-%d/cmd-%d/fallback-%s'%(cache,cmd,fb))
missing=neg-failed
assert not missing, 'baseline did not fail expected negative controls: '+', '.join(sorted(missing))
assert pos<=passed, 'baseline positive controls (complete first key) did not pass: '+', '.join(sorted(pos-passed))
assert 'TestQZTrojanFallbackCompatibility' in passed, 'fallback compatibility controls must already pass on the base'
print('Unpatched pinned core: %d fragmented/read-ahead negative controls failed; %d complete-key positive controls and fallback compatibility passed'%(len(neg),len(pos)))
PY
  git checkout --quiet -- . && git clean -fdq
fi
bash "$root/apply-transport-buffer-patch.sh" "$PWD"
bash "$root/apply-trojan-handshake-patch.sh" "$PWD"
cp "$tests/fragment_test.go.txt" transport/trojan/qz_trojan_fragment_test.go
cp "$tests/handshake_test.go.txt" transport/trojan/qz_trojan_handshake_test.go
cp "$tests/transport_test.go.txt" transport/trojan/qz_trojan_transport_test.go
cp "$tests/transport_grpc_quic_test.go.txt" transport/trojan/qz_trojan_transport_grpc_quic_test.go
if ! "$GO" test -mod=readonly -race -tags "$tags" -timeout=20m -run '^TestQZTrojan' -count="$rounds" -json "$package" > "$work/fixed.json"; then
  python3 - "$work/fixed.json" <<'PY' >&2 || true
import json,sys
for line in open(sys.argv[1]):
    e=json.loads(line)
    if e.get('Action')=='output' and ('FAIL' in e.get('Output','') or '_test.go' in e.get('Output','')): sys.stdout.write(e['Output'])
PY
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
expected={'TestQZTrojanFragmentedRequest','TestQZTrojanReadAheadCache','TestQZTrojanFallbackCompatibility','TestQZTrojanEOFAndMalformed','TestQZTrojanNoProgress','TestQZTrojanPreCancelled','TestQZTrojanEarlierConnDeadlineWins','TestQZTrojanContextCancelMidHandshake','TestQZTrojanBoundedBudget','TestQZTrojanLongConnectionUnaffected','TestQZTrojanConcurrentRandomFragments','TestQZTrojanRealTransports','TestQZTrojanRealTransportsTagged'}
rounds=int(sys.argv[2])
assert not skipped, 'skipped: '+str(skipped)
assert expected<=passed.keys() and all(passed[n]==rounds for n in expected), 'missing or partial groups'
assert ran==passed, 'a started regression did not pass'
for transport in ('ws','httpupgrade','http2','grpc-lite'):
    assert passed['TestQZTrojanRealTransports/'+transport+'/stalled-handshake-cleanup']==rounds
for transport in ('grpc','quic'):
    assert passed['TestQZTrojanRealTransportsTagged/'+transport+'/stalled-handshake-cleanup']==rounds
print('Patched core: %d race rounds for each of %d groups (%d leaf cases per round); no skips, no retries'%(rounds,len(expected),len([n for n in passed if '/' in n])))
for n in sorted(expected):print('PASS '+n+' x'+str(passed[n]))
PY
