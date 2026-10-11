#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
cat >"$fixture/go" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$FIXTURE_SUMMARY"
exit "$FIXTURE_EXIT"
EOF
chmod +x "$fixture/go"
check_case() {
  local summary=$1 status=$2 expected=$3 actual=0
  PATH="$fixture:$PATH" FIXTURE_SUMMARY="$summary" FIXTURE_EXIT="$status" \
    bash "$root/tests/sanity/test-sanity.sh" >"$fixture/output" 2>&1 || actual=$?
  if [[ $actual != "$expected" ]]; then
    cat "$fixture/output" >&2
    echo "Unexpected runner status $actual (expected $expected)" >&2
    exit 1
  fi
}
check_case '81 Passed | 0 Failed' 0 0
check_case '81 Passed | 1 Failed' 0 1
check_case '81 Passed | 0 Failed' 1 1
check_case 'panic: test crashed' 2 1
check_case '10 Passed | 0 Failed' 0 1
