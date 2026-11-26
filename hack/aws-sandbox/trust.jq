# foreign($account; $rules) reads a trust policy and is true when one of its
# Allow statements trusts a principal that no run of the sandbox trusts:
# anyone, another account, an identity provider of another account or of a
# host of $rules.untrusted_provider_hosts, or a service that is not in
# $rules.trusted_services. A role of a run trusts only the OIDC issuer of its
# kind cluster or EKS Pod Identity. $rules is sweep-rules.json. sweep.sh and
# leftovers.sh include it with jq -L. hack/aws-sandbox/sweeper/sweeper.py
# holds the same check for the scheduled sweep in AWS.
def foreign($account; $rules):
  def list: if type == "array" then . else [.] end;
  def own: test("^arn:aws[a-z-]*:(iam|sts)::" + $account + ":");
  def untrusted_host: . as $arn | any($rules.untrusted_provider_hosts[]; . as $h | $arn | endswith(":oidc-provider/" + $h));
  [(.Statement // []) | list | .[] | select(.Effect == "Allow")
    | if has("NotPrincipal") or (.Principal | type) != "object" then true
      else .Principal | to_entries[] | .key as $kind | .value | list | .[]
        | if $kind == "AWS" then (own or . == $account) | not
          elif $kind == "Federated" then (own and test(":oidc-provider/") and (untrusted_host | not)) | not
          elif $kind == "Service" then . as $s | any($rules.trusted_services[]; . == $s) | not
          else true
          end
      end] | any;
