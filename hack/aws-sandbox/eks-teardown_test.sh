#!/usr/bin/env bash
# Tests eks-teardown.sh with stubs of the three scripts that it runs, in a
# copy of this directory. make test-scripts runs it.
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp "$here/eks-teardown.sh" "$work/"

# Each stub writes its name and arguments to calls, and exits with the code
# in the file <name>.code, 0 without it.
for script in eks-sweep sweep leftovers; do
  cat > "$work/$script.sh" << STUB
#!/usr/bin/env bash
echo "$script \$*" >> "$work/calls"
exit "\$(cat "$work/$script.code" 2> /dev/null || echo 0)"
STUB
  chmod +x "$work/$script.sh"
done

failed=0
fail() {
  echo "FAIL $1: $2" >&2
  failed=1
}

name="a run that left nothing"
"$work/eks-teardown.sh" cpo-ci-1-1-eks > /dev/null 2>&1
code=$?
[ "$code" -eq 0 ] || fail "$name" "exit $code, want 0"
want="eks-sweep --run cpo-ci-1-1-eks
sweep --run cpo-ci-1-1-eks
leftovers --eks cpo-ci-1-1-eks"
[ "$(cat "$work/calls")" = "$want" ] || fail "$name" "ran '$(cat "$work/calls")', want '$want'"

for script in eks-sweep sweep leftovers; do
  name="a teardown where $script fails"
  rm -f "$work/calls" "$work"/*.code
  echo 1 > "$work/$script.code"
  "$work/eks-teardown.sh" cpo-ci-1-1-eks > /dev/null 2>&1
  code=$?
  [ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"
  [ "$(wc -l < "$work/calls")" -eq 3 ] || fail "$name" "ran '$(cat "$work/calls")', want all three"
done

name="a run ID that does not match"
rm -f "$work/calls" "$work"/*.code
"$work/eks-teardown.sh" BAD > /dev/null 2>&1
code=$?
[ "$code" -eq 2 ] || fail "$name" "exit $code, want 2"
[ ! -e "$work/calls" ] || fail "$name" "ran '$(cat "$work/calls")'"

exit "$failed"
