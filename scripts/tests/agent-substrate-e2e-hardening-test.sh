#!/usr/bin/env bash
# Exercise cleanup and preflight without a cluster or provider credentials.
set -Eeuo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/orka-native-substrate-test.XXXXXX")"
trap 'rm -rf "${test_root}"' EXIT
source "${root}/scripts/agent-substrate-e2e.sh"

# A failed API read is not proof of deletion.
kubectl() { return 7; }
if wait_absent task gone; then
  echo 'cleanup treated an API error as absence' >&2
  exit 1
fi
kubectl() { return 0; }
wait_absent task gone
unset -f kubectl

mkdir -p "${test_root}/tools"
cat >"${test_root}/tools/docker" <<'SH'
#!/usr/bin/env bash
exit 1
SH
cat >"${test_root}/tools/git" <<'SH'
#!/usr/bin/env bash
printf 'unexpected provider access\n' >>"${ORKA_SUBSTRATE_TEST_CALLS}"
exit 99
SH
chmod +x "${test_root}/tools/docker" "${test_root}/tools/git"
export ORKA_SUBSTRATE_TEST_CALLS="${test_root}/unexpected-calls"
original_path="${PATH}"
export PATH="${test_root}/tools:${PATH}"
# Calling in a conditional disables errexit inside a Bash function. The
# preflight must still return failure before creating or installing anything.
if substrate_prepare_upstream "${root}" "${test_root}/run" guarded-cluster 2>"${test_root}/error"; then
  echo 'preflight ignored an unavailable Docker engine' >&2
  exit 1
fi
[[ ! -e "${ORKA_SUBSTRATE_TEST_CALLS}" ]]
[[ ! -e "${test_root}/run" ]]
rg -q 'Docker engine is unavailable' "${test_root}/error"
# A working Docker stub cannot authorize a provider fork or a movable ref.
printf '#!/usr/bin/env bash\nexit 0\n' >"${test_root}/tools/docker"
for selection in fork branch; do
  if [[ "${selection}" == fork ]]; then
    export SUBSTRATE_REPO=https://example.invalid/provider-fork.git
    unset SUBSTRATE_REF
  else
    unset SUBSTRATE_REPO
    export SUBSTRATE_REF=main
  fi
  if substrate_prepare_upstream "${root}" "${test_root}/run" guarded-cluster 2>"${test_root}/error"; then
    echo 'preflight accepted an unsupported provider source' >&2
    exit 1
  fi
  [[ ! -e "${ORKA_SUBSTRATE_TEST_CALLS}" ]]
  rg -q 'provider forks and patches are unsupported' "${test_root}/error"
done
unset SUBSTRATE_REPO SUBSTRATE_REF
export PATH="${original_path}"
source "${root}/hack/agent-substrate/upstream.env"
[[ "$(shasum -a 256 "${root}/internal/substratepb/ateapi.proto" | awk '{print $1}')" == "${SUBSTRATE_UPSTREAM_PROTO_SHA256}" ]]
printf 'Native Substrate source, preflight, and absence checks passed\n'
