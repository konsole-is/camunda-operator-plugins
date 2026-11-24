#!/usr/bin/env bash
# The checks of aws-s3 that need real S3, after the suite:
#   - many-objects: a bucket that one delete pass cannot empty ends deleted
#     over several passes
#   - versioned: a bucket with versioning turned on by hand ends deleted
#   - steady: no request records DriftCorrected after the writes of the
#     plugin, over two resyncs, and a change by hand gets DriftCorrected
#
# The values of the run set s3.deleteBudget to 1ms, so one pass deletes one
# batch of 1,000 objects, and s3.resync to 30s. Needs RUN_ID, AWS_ACCOUNT_ID,
# AWS_REGION, RUN_VARS, and the credentials of the CI role.
set -euo pipefail

: "${RUN_ID:?RUN_ID is not set}"
: "${AWS_ACCOUNT_ID:?AWS_ACCOUNT_ID is not set}"
: "${AWS_REGION:?AWS_REGION is not set}"
: "${RUN_VARS:?RUN_VARS is not set}"
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/../../.." && pwd)
ns=conformance-aws-s3-checks
resync=30
# The settle time of the plugin, settleTime in internal/awss3/settle.go.
settle=300
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

failed=0
fail() {
  echo "FAIL: $*" >&2
  failed=1
}

# bucket_of <request> prints the bucket output of the request.
bucket_of() {
  kubectl get -n "$ns" "pluginrequest/$1" -o jsonpath='{.status.outputs.bucket}'
}

# bucket_gone <bucket> waits up to 60 seconds for HeadBucket to answer 404.
bucket_gone() {
  local out
  for _ in $(seq 12); do
    if ! out=$(aws s3api head-bucket --bucket "$1" 2>&1); then
      [[ $out == *"(404)"* ]] && return 0
      echo "$out" >&2
    fi
    sleep 5
  done
  return 1
}

# drift_events [<namespace> <request>] prints the DriftCorrected events of
# the requests of aws-s3, or of one request. The other controllers of the
# release record their own DriftCorrected events in other namespaces.
drift_events() {
  if [ $# -eq 0 ]; then
    for n in conformance-aws-s3 "$ns"; do
      kubectl get events -n "$n" --field-selector reason=DriftCorrected -o name
    done
  else
    kubectl get events -n "$1" --field-selector "reason=DriftCorrected,involvedObject.name=$2" -o name
  fi
}

# delete_request <request> deletes the request and waits up to 6 minutes for
# it to go.
delete_request() {
  kubectl delete -n "$ns" "pluginrequest/$1" --wait=false
  kubectl wait -n "$ns" "pluginrequest/$1" --for=delete --timeout=6m ||
    fail "the request $1 was not deleted within 6 minutes"
}

check_many_objects() {
  echo "==> Checking that a bucket that one delete pass cannot empty ends deleted"
  local bucket count watch
  bucket=$(bucket_of many-objects)
  mkdir "$work/many"
  for i in $(seq 2500); do
    : > "$work/many/$i"
  done
  aws s3 cp "$work/many" "s3://$bucket/many/" --recursive --only-show-errors ||
    { fail "could not put 2500 objects into the bucket $bucket"; return; }
  count=$(aws s3 ls "s3://$bucket/many/" | wc -l)
  if [ "$count" -ne 2500 ]; then
    fail "the bucket $bucket holds $count objects, want 2500"
    return
  fi

  # With -w, a jsonpath that filters with range prints only the first object.
  kubectl get -n "$ns" pluginrequest/many-objects -w -o json > "$work/many.json" &
  watch=$!
  delete_request many-objects
  kill "$watch" 2> /dev/null || true
  wait "$watch" 2> /dev/null || true
  jq -r '.status.conditions[]? | select(.type == "Ready") | "\(.reason) \(.message)"' \
    "$work/many.json" > "$work/many.ready" || true

  # 2,500 objects take three passes or more, about one second apart, and the
  # watch sees each status write.
  if ! grep -qxF "Deleting Deleting the objects of the bucket $bucket" "$work/many.ready"; then
    fail "Ready of many-objects never showed Deleting the objects of the bucket $bucket"
    echo "Ready of many-objects during the delete:" >&2
    cat "$work/many.ready" >&2
  fi
  bucket_gone "$bucket" || fail "the bucket $bucket of many-objects stayed"
}

check_versioned() {
  echo "==> Checking that a bucket with versioning turned on by hand ends deleted"
  local bucket counts
  bucket=$(bucket_of versioned)
  echo data > "$work/object"
  {
    aws s3api put-bucket-versioning --bucket "$bucket" --versioning-configuration Status=Enabled &&
      aws s3api put-object --bucket "$bucket" --key a --body "$work/object" > /dev/null &&
      aws s3api put-object --bucket "$bucket" --key a --body "$work/object" > /dev/null &&
      aws s3api put-object --bucket "$bucket" --key b --body "$work/object" > /dev/null &&
      aws s3api delete-object --bucket "$bucket" --key a > /dev/null
  } || { fail "could not write the versions of the bucket $bucket"; return; }
  counts=$(aws s3api list-object-versions --bucket "$bucket" \
    --query '[length(Versions), length(DeleteMarkers)]' --output text)
  if [ "$counts" != $'3\t1' ]; then
    fail "the bucket $bucket holds '$counts' versions and delete markers, want 3 and 1"
    return
  fi

  delete_request versioned
  bucket_gone "$bucket" || fail "the versioned bucket $bucket stayed"
}

check_steady() {
  echo "==> Checking that no request records DriftCorrected after the writes of the plugin"
  local since left events drift bucket
  # When the settle time after its writes ends, the plugin checks the bucket
  # once more, also without a resync. Two resyncs run after that.
  since=$(kubectl get -n "$ns" pluginrequest/steady \
    -o jsonpath='{.status.conditions[?(@.type=="OutputsReady")].lastTransitionTime}')
  left=$(($(date -d "$since" +%s) + settle + 2 * resync + 15 - $(date +%s)))
  if [ "$left" -gt 0 ]; then
    sleep "$left"
  fi
  events=$(drift_events)
  [ -z "$events" ] ||
    fail "requests of the run recorded DriftCorrected after the writes of the plugin: $events"
  drift=$(kubectl get -n "$ns" pluginrequest/steady \
    -o jsonpath='{.status.summary[?(@.label=="Last drift corrected")].value}')
  [ -z "$drift" ] || fail "steady shows Last drift corrected $drift"

  # Without a resync, the check above passes on nothing. After the settle
  # time, only a resync reconciles the request again.
  echo "==> Checking that a lifecycle rule removed by hand gets DriftCorrected"
  bucket=$(bucket_of steady)
  aws s3api delete-bucket-lifecycle --bucket "$bucket" ||
    { fail "could not remove the lifecycle rule of the bucket $bucket"; return; }
  drift=""
  for _ in $(seq 18); do
    drift=$(drift_events "$ns" steady)
    [ -n "$drift" ] && break
    sleep 10
  done
  [ -n "$drift" ] ||
    fail "steady did not record DriftCorrected within 3 minutes after the lifecycle rule was removed by hand, so no resync ran"
  delete_request steady
}

kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
"$repo/test/conformance/render.sh" "$here/s3-requests.yaml" | kubectl apply -f -
# No request of aws-irsa names the clusters of these requests, so no
# identity hub marks their Grants Ready, and Ready stays False with
# WaitingForGrants.
for r in many-objects versioned steady; do
  kubectl wait -n "$ns" "pluginrequest/$r" --for=condition=OutputsReady --timeout=6m
done

check_many_objects
check_versioned
check_steady

if [ "$failed" -ne 0 ]; then
  kubectl get -n "$ns" pluginrequests -o yaml || true
  kubectl get events -n "$ns" --sort-by=.lastTimestamp || true
fi
kubectl delete namespace "$ns" --timeout=3m || failed=1
exit "$failed"
