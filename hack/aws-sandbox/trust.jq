# foreign($account) reads a trust policy and is true when one of its Allow
# statements trusts a principal that no run of the sandbox trusts: anyone,
# another account, an identity provider of another account or of GitHub, or
# a service other than EKS Pod Identity. A role of a run trusts only the OIDC
# issuer of its kind cluster or EKS Pod Identity. sweep.sh and leftovers.sh
# include it with jq -L.
def foreign($account):
  def list: if type == "array" then . else [.] end;
  def own: test("^arn:aws[a-z-]*:(iam|sts)::" + $account + ":");
  [(.Statement // []) | list | .[] | select(.Effect == "Allow")
    | if has("NotPrincipal") or (.Principal | type) != "object" then true
      else .Principal | to_entries[] | .key as $kind | .value | list | .[]
        | if $kind == "AWS" then (own or . == $account) | not
          elif $kind == "Federated" then
            (own and test(":oidc-provider/") and (test(":oidc-provider/token\\.actions\\.githubusercontent\\.com$") | not)) | not
          elif $kind == "Service" then . != "pods.eks.amazonaws.com"
          else true
          end
      end] | any;
