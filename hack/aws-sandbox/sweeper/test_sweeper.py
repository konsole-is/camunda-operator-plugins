"""Tests of sweeper.py against fakes of the AWS clients. No test calls AWS.
make test-sweeper runs them."""

import datetime
import unittest

import sweeper

RULES = sweeper.load_rules()
ACCOUNT = "111122223333"
NOW = datetime.datetime(2026, 11, 24, 12, 0, tzinfo=datetime.timezone.utc)
OLD = NOW - datetime.timedelta(hours=7)
YOUNG = NOW - datetime.timedelta(hours=1)
ENV = {"AWS_REGION": "eu-west-1", "AWS_KMS_REPLICA_REGION": "eu-central-1", "AWS_EKS_VPC_ID": "vpc-1",
       "AWS_EKS_REPOSITORIES": "cpo-sandbox-eks/aws", "AWS_EKS_SCHEDULE_GROUP": "cpo-sandbox-eks"}

ISSUER = {"Statement": [{"Effect": "Allow", "Action": "sts:AssumeRoleWithWebIdentity", "Principal": {
    "Federated": f"arn:aws:iam::{ACCOUNT}:oidc-provider/b.s3.eu-west-1.amazonaws.com/cpo-ci-1"}}]}
PODS = {"Statement": {"Effect": "Allow", "Action": ["sts:AssumeRole"], "Principal": {"Service": "pods.eks.amazonaws.com"}}}
OTHER_ACCOUNT = {"Statement": [{"Effect": "Allow", "Principal": {"AWS": "arn:aws:iam::999999999999:root"}}]}


def tags(value):
    return [] if value is None else [{"Key": "cpo-ci-run", "Value": value}]


class ClientError(Exception):
    def __init__(self, code):
        super().__init__(code)
        self.response = {"Error": {"Code": code}}


class Fake:
    """A fake client: each list operation answers from pages, each other
    operation from answers, and every call is recorded in calls."""

    def __init__(self, calls, pages=None, answers=None):
        self.calls, self.pages, self.answers = calls, pages or {}, answers or {}

    def get_paginator(self, operation):
        fake = self

        class Paginator:
            def paginate(self, **kwargs):
                fake.calls.append((operation, kwargs))
                return [fake.pages.get(operation, {})]

        return Paginator()

    def __getattr__(self, operation):
        def call(**kwargs):
            self.calls.append((operation, kwargs))
            answer = self.answers.get(operation, {})
            answer = answer(**kwargs) if callable(answer) else answer
            if isinstance(answer, Exception):
                raise answer
            return answer

        return call


def sweep(**clients):
    """Runs one pass with the fake clients of clients, keyed by service or
    by service and region, and returns the pass and the calls."""
    calls = []
    fakes = {}

    def factory(service, region):
        key = (service, region)
        if key not in fakes:
            spec = clients.get(f"{service}@{region}", clients.get(service, {}))
            fakes[key] = Fake(calls, spec.get("pages"), spec.get("answers"))
        return fakes[key]

    clients.setdefault("sts", {"answers": {"get_caller_identity": {"Account": ACCOUNT}}})
    clients.setdefault("iam", {"answers": {"list_open_id_connect_providers": {"OpenIDConnectProviderList": []}}})
    clients.setdefault("s3", {"answers": {"list_buckets": {"Buckets": []}}})
    run = sweeper.Sweep(factory, RULES, ENV, now=NOW, log=LOG.append)
    LOG.clear()
    run.run()
    return run, calls


# The lines that the last pass of sweep logged.
LOG = []


def called(calls, operation):
    return [kwargs for op, kwargs in calls if op == operation]


class Rules(unittest.TestCase):
    def test_run_tag_reads_the_key_in_any_case(self):
        self.assertEqual(sweeper.run_tag([{"Key": "CPO-CI-RUN", "Value": "cpo-ci-1"}], RULES), "cpo-ci-1")
        self.assertEqual(sweeper.run_tag([{"TagKey": "cpo-ci-run", "TagValue": "cpo-ci-2"}], RULES), "cpo-ci-2")
        self.assertIsNone(sweeper.run_tag([{"Key": "team", "Value": "a"}], RULES))

    def test_an_empty_or_none_run_value_is_no_run(self):
        for value in (None, "", "None"):
            self.assertFalse(sweeper.matches(value, OLD, NOW, 6, RULES), value)
        self.assertTrue(sweeper.matches("foo", OLD, NOW, 6, RULES))

    def test_a_young_resource_stays(self):
        self.assertFalse(sweeper.matches("cpo-ci-1", YOUNG, NOW, 6, RULES))
        self.assertTrue(sweeper.older(OLD.timestamp(), NOW, 6))

    def test_trusts_of_runs(self):
        for document in (ISSUER, PODS, None, {"Statement": [{"Effect": "Deny", "Principal": "*"}]}):
            self.assertFalse(sweeper.foreign_trust(document, ACCOUNT, RULES), document)

    def test_trusts_of_no_run(self):
        for principal in ("*", {"AWS": "arn:aws:iam::999999999999:root"}, {"Service": "ec2.amazonaws.com"},
                          {"Service": ["pods.eks.amazonaws.com", "ec2.amazonaws.com"]},
                          {"Federated": f"arn:aws:iam::{ACCOUNT}:oidc-provider/token.actions.githubusercontent.com"},
                          {"Federated": "arn:aws:iam::999999999999:oidc-provider/example.com"},
                          {"Federated": "cognito-identity.amazonaws.com"}):
            document = {"Statement": [{"Effect": "Allow", "Principal": principal}]}
            self.assertTrue(sweeper.foreign_trust(document, ACCOUNT, RULES), principal)
        self.assertTrue(sweeper.foreign_trust({"Statement": [{"Effect": "Allow", "NotPrincipal": {"AWS": ACCOUNT}}]},
                                              ACCOUNT, RULES))

    def test_a_role_with_a_trust_of_no_run_goes_at_any_age(self):
        role = {"RoleName": "cpo-ci-1-hub-a", "CreateDate": YOUNG, "AssumeRolePolicyDocument": OTHER_ACCOUNT}
        self.assertEqual(sweeper.select_role(role, None, ACCOUNT, NOW, RULES), "a trust of no run")

    def test_the_roles_that_stay(self):
        cases = [
            ({"RoleName": "cpo-ci-1-hub-a", "CreateDate": YOUNG, "AssumeRolePolicyDocument": ISSUER}, "cpo-ci-1"),
            ({"RoleName": "cpo-ci-1-hub-a", "CreateDate": OLD, "AssumeRolePolicyDocument": ISSUER}, "None"),
            ({"RoleName": "cpo-ci-1-hub-a", "CreateDate": OLD, "AssumeRolePolicyDocument": ISSUER}, ""),
            ({"RoleName": "cpo-sandbox-sweep", "CreateDate": OLD, "AssumeRolePolicyDocument": OTHER_ACCOUNT},
             "cpo-ci-1"),
        ]
        for role, tag in cases:
            self.assertIsNone(sweeper.select_role(role, tag, ACCOUNT, NOW, RULES), (role["RoleName"], tag))

    def test_an_old_role_of_a_run_goes(self):
        role = {"RoleName": "cpo-ci-1-hub-a", "CreateDate": OLD, "AssumeRolePolicyDocument": PODS}
        self.assertEqual(sweeper.select_role(role, "cpo-ci-1", ACCOUNT, NOW, RULES), "run cpo-ci-1")

    def test_buckets(self):
        self.assertTrue(sweeper.select_bucket("cpo-ci-1-backup", OLD, NOW, RULES))
        self.assertFalse(sweeper.select_bucket("cpo-ci-1-backup", YOUNG, NOW, RULES))
        self.assertFalse(sweeper.select_bucket("cpo-sandbox-issuers-1", OLD, NOW, RULES))

    def test_clusters(self):
        old = NOW - datetime.timedelta(hours=3)
        run = {"tags": {"cpo-ci-run": "cpo-ci-1-eks"}, "createdAt": old, "resourcesVpcConfig": {"vpcId": "vpc-2"}}
        self.assertTrue(sweeper.select_cluster("cpo-ci-1-eks", run, "vpc-1", NOW, RULES))
        self.assertFalse(sweeper.select_cluster("prod", run, "vpc-1", NOW, RULES), "another name in another VPC")
        self.assertTrue(sweeper.select_cluster("prod", dict(run, resourcesVpcConfig={"vpcId": "vpc-1"}), "vpc-1",
                                               NOW, RULES), "another name in the VPC of the runs")
        for value in ("prod", "None", ""):
            self.assertFalse(sweeper.select_cluster("cpo-ci-1-eks", dict(run, tags={"cpo-ci-run": value}), "vpc-1",
                                                    NOW, RULES), value)
        self.assertFalse(sweeper.select_cluster("cpo-ci-1-eks", dict(run, createdAt=YOUNG), "vpc-1", NOW, RULES))

    def test_an_eks_resource_needs_the_value_of_a_run(self):
        old = NOW - datetime.timedelta(hours=3)
        for value in ("prod", "cpo-ci-", "None", "", None):
            self.assertFalse(sweeper.select_eks(value, old, NOW, RULES), value)
        self.assertTrue(sweeper.select_eks("cpo-ci-1-eks", old, NOW, RULES))

    def test_a_cluster_tag_key_in_any_case(self):
        old = NOW - datetime.timedelta(hours=3)
        cluster = {"tags": {"CPO-CI-RUN": "cpo-ci-1-eks"}, "createdAt": old}
        self.assertTrue(sweeper.select_cluster("cpo-ci-1-eks", cluster, "vpc-1", NOW, RULES))

    def test_names(self):
        self.assertEqual(sweeper.schedule_owner("cpo-ci-1-eks-delete-cluster"), "cpo-ci-1-eks")
        self.assertEqual(sweeper.schedule_owner("cpo-ci-1-eks-terminate-nodes"), "cpo-ci-1-eks")
        self.assertEqual(sweeper.security_group_cluster({"GroupName": "eks-cluster-sg-cpo-ci-1-eks-123"}),
                         "cpo-ci-1-eks")
        tagged = {"GroupName": "eks-cluster-sg-other-123", "Tags": [{"Key": "aws:eks:cluster-name", "Value": "other"}]}
        self.assertEqual(sweeper.security_group_cluster(tagged), "other")

    def test_an_untagged_role_with_a_trust_of_no_run_goes(self):
        role = {"RoleName": "cpo-ci-1-hub-a", "CreateDate": YOUNG, "AssumeRolePolicyDocument": OTHER_ACCOUNT}
        self.assertEqual(sweeper.select_role(role, None, ACCOUNT, NOW, RULES), "a trust of no run")


class Pass(unittest.TestCase):
    def test_roles(self):
        roles = [
            {"RoleName": "cpo-ci-1-hub-young", "CreateDate": YOUNG, "AssumeRolePolicyDocument": ISSUER},
            {"RoleName": "cpo-ci-1-hub-foreign", "CreateDate": YOUNG, "AssumeRolePolicyDocument": OTHER_ACCOUNT},
            {"RoleName": "cpo-ci-1-hub-old", "CreateDate": OLD, "AssumeRolePolicyDocument": ISSUER},
            {"RoleName": "cpo-ci-1-hub-none", "CreateDate": OLD, "AssumeRolePolicyDocument": ISSUER},
            {"RoleName": "cpo-sandbox-eks", "CreateDate": OLD, "AssumeRolePolicyDocument": OTHER_ACCOUNT},
        ]
        role_tags = {"cpo-ci-1-hub-none": tags("None")}
        iam = {"pages": {"list_roles": {"Roles": roles}}, "answers": {
            "list_role_tags": lambda RoleName: {"Tags": role_tags.get(RoleName, tags("cpo-ci-1"))},
            "list_role_policies": {"PolicyNames": ["grant-a"]},
            "list_attached_role_policies": {"AttachedPolicies": []},
            "list_open_id_connect_providers": {"OpenIDConnectProviderList": []}}}
        run, calls = sweep(iam=iam)
        self.assertEqual(sorted(c["RoleName"] for c in called(calls, "delete_role")),
                         ["cpo-ci-1-hub-foreign", "cpo-ci-1-hub-old"])
        self.assertEqual(run.failures, [])

    def test_an_untagged_role_with_a_trust_of_no_run_is_removed_from_its_profiles(self):
        iam = {"pages": {"list_roles": {"Roles": [
            {"RoleName": "cpo-ci-1-hub-x", "CreateDate": YOUNG, "AssumeRolePolicyDocument": OTHER_ACCOUNT}]}},
            "answers": {"list_role_tags": {"Tags": []},
                        "list_instance_profiles_for_role": {"InstanceProfiles": [{"InstanceProfileName": "cpo-ci-1-p"}]},
                        "list_open_id_connect_providers": {"OpenIDConnectProviderList": []}}}
        run, calls = sweep(iam=iam)
        self.assertEqual(called(calls, "remove_role_from_instance_profile"),
                         [{"InstanceProfileName": "cpo-ci-1-p", "RoleName": "cpo-ci-1-hub-x"}])
        self.assertEqual(called(calls, "delete_role"), [{"RoleName": "cpo-ci-1-hub-x"}])
        self.assertIn("remove role cpo-ci-1-hub-x: a trust of no run", LOG)
        self.assertEqual(run.failures, [])

    def test_a_role_whose_policy_another_sweep_deleted_first_is_still_deleted(self):
        iam = {"pages": {"list_roles": {"Roles": [
            {"RoleName": "cpo-ci-1-hub-x", "CreateDate": YOUNG, "AssumeRolePolicyDocument": OTHER_ACCOUNT}]}},
            "answers": {"list_role_tags": {"Tags": []}, "list_role_policies": {"PolicyNames": ["grant-a"]},
                        "delete_role_policy": ClientError("NoSuchEntity"),
                        "list_open_id_connect_providers": {"OpenIDConnectProviderList": []}}}
        run, calls = sweep(iam=iam)
        self.assertEqual(called(calls, "delete_role"), [{"RoleName": "cpo-ci-1-hub-x"}])
        self.assertEqual(run.failures, [])

    def test_the_log_of_a_pass(self):
        iam = {"pages": {"list_roles": {"Roles": [
            {"RoleName": "cpo-ci-1-hub-old", "CreateDate": OLD, "AssumeRolePolicyDocument": ISSUER}]}},
            "answers": {"list_role_tags": {"Tags": tags("cpo-ci-1")}, "delete_role": ClientError("AccessDenied"),
                        "list_open_id_connect_providers": {"OpenIDConnectProviderList": []}}}
        sweep(iam=iam)
        self.assertIn("listed 1 roles of runs", LOG)
        self.assertIn("remove role cpo-ci-1-hub-old: run cpo-ci-1, 7.0 h old", LOG)
        self.assertTrue([line for line in LOG if line.startswith("failed: remove role cpo-ci-1-hub-old: ")], LOG)
        self.assertEqual(LOG[-1], "pass done: 1 removals, 1 failures")

    def test_an_oidc_provider_that_the_sweep_may_not_read_is_no_failure_and_another_error_is(self):
        providers = {"OpenIDConnectProviderList": [{"Arn": f"arn:aws:iam::{ACCOUNT}:oidc-provider/a"},
                                                   {"Arn": f"arn:aws:iam::{ACCOUNT}:oidc-provider/b"}]}
        errors = {"a": ClientError("AccessDenied"), "b": ClientError("ServiceFailure")}
        iam = {"answers": {"list_open_id_connect_providers": providers,
                           "get_open_id_connect_provider": lambda OpenIDConnectProviderArn: errors[
                               OpenIDConnectProviderArn.rsplit("/", 1)[1]]}}
        run, _ = sweep(iam=iam)
        self.assertEqual(len(run.failures), 1)
        self.assertIn("oidc-provider/b", run.failures[0])

    def test_a_role_that_another_sweep_deleted_is_no_failure(self):
        iam = {"pages": {"list_roles": {"Roles": [
            {"RoleName": "cpo-ci-1-hub-gone", "CreateDate": OLD, "AssumeRolePolicyDocument": ISSUER}]}},
            "answers": {"list_role_tags": ClientError("NoSuchEntity"),
                        "list_open_id_connect_providers": {"OpenIDConnectProviderList": []}}}
        run, calls = sweep(iam=iam)
        self.assertEqual(called(calls, "delete_role"), [])
        self.assertEqual(run.failures, [])

    def test_a_failed_delete_is_a_failure(self):
        iam = {"pages": {"list_roles": {"Roles": [
            {"RoleName": "cpo-ci-1-hub-old", "CreateDate": OLD, "AssumeRolePolicyDocument": ISSUER}]}},
            "answers": {"list_role_tags": {"Tags": tags("cpo-ci-1")}, "list_role_policies": {"PolicyNames": []},
                        "list_attached_role_policies": {"AttachedPolicies": []},
                        "delete_role": ClientError("AccessDenied"),
                        "list_open_id_connect_providers": {"OpenIDConnectProviderList": []}}}
        run, _ = sweep(iam=iam)
        self.assertEqual(len(run.failures), 1)

    def test_keys_in_both_regions(self):
        def region(key, state, created):
            return {"pages": {"get_resources": {"ResourceTagMappingList": [
                {"ResourceARN": f"arn:aws:kms:x:{ACCOUNT}:key/{key}", "Tags": tags("cpo-ci-1")},
                {"ResourceARN": f"arn:aws:kms:x:{ACCOUNT}:key/{key}-none", "Tags": tags("None")}]}}}, {
                "answers": {"describe_key": {"KeyMetadata": {"KeyState": state, "CreationDate": created}},
                            "list_aliases": {"Aliases": [{"AliasName": "alias/cpo-ci-1/a"}]}}}

        tagging_home, kms_home = region("k-home", "Enabled", OLD)
        tagging_replica, kms_replica = region("k-replica", "PendingDeletion", OLD)
        _, calls = sweep(**{"resourcegroupstaggingapi@eu-west-1": tagging_home, "kms@eu-west-1": kms_home,
                            "resourcegroupstaggingapi@eu-central-1": tagging_replica,
                            "kms@eu-central-1": kms_replica})
        self.assertEqual(called(calls, "schedule_key_deletion"), [{"KeyId": "k-home", "PendingWindowInDays": 7}])
        self.assertEqual(called(calls, "delete_alias"), [{"AliasName": "alias/cpo-ci-1/a"}])
        self.assertNotIn({"KeyId": "k-home-none"}, called(calls, "describe_key"))

    def test_eks_parts(self):
        old = NOW - datetime.timedelta(hours=3)
        ec2 = {"pages": {
            "describe_instances": {"Reservations": [{"Instances": [
                {"InstanceId": "i-old", "LaunchTime": old, "Tags": tags("cpo-ci-1-eks")},
                {"InstanceId": "i-young", "LaunchTime": YOUNG, "Tags": tags("cpo-ci-2-eks")}]}]},
            "describe_volumes": {"Volumes": [
                {"VolumeId": "v-free", "State": "available", "CreateTime": old, "Tags": tags("cpo-ci-1-eks")},
                {"VolumeId": "v-used", "State": "in-use", "CreateTime": old, "Tags": tags("cpo-ci-1-eks")},
                {"VolumeId": "v-none", "State": "available", "CreateTime": old, "Tags": tags("None")}]},
            "describe_network_interfaces": {"NetworkInterfaces": [{"NetworkInterfaceId": "eni-1", "Status": "available"}]},
            "describe_security_groups": {"SecurityGroups": [
                {"GroupId": "sg-1", "GroupName": "eks-cluster-sg-cpo-ci-1-eks-1"},
                {"GroupId": "sg-2", "GroupName": "eks-cluster-sg-cpo-ci-2-eks-1"},
                {"GroupId": "sg-3", "GroupName": "eks-cluster-sg-other-1",
                 "Tags": [{"Key": "aws:eks:cluster-name", "Value": "other"}]}]}},
            "answers": {"delete_security_group": lambda GroupId: (
                ClientError("DependencyViolation") if GroupId == "sg-3" else {})}}
        eks = {"pages": {"list_clusters": {"clusters": ["cpo-ci-2-eks"]}}, "answers": {"describe_cluster": {
            "cluster": {"status": "ACTIVE", "createdAt": YOUNG, "tags": {"cpo-ci-run": "cpo-ci-2-eks"}}}}}
        scheduler = {"pages": {"list_schedules": {"Schedules": [
            {"Name": "cpo-ci-1-eks-terminate-nodes", "CreationDate": old},
            {"Name": "cpo-ci-2-eks-delete-cluster", "CreationDate": old},
            {"Name": "cpo-ci-3-eks-delete-cluster", "CreationDate": old}]}}}
        run, calls = sweep(ec2=ec2, eks=eks, scheduler=scheduler)
        self.assertEqual(called(calls, "terminate_instances"), [{"InstanceIds": ["i-old"]}])
        listings = called(calls, "describe_instances") + called(calls, "describe_volumes")
        self.assertFalse([f for c in listings for f in c.get("Filters", []) if f["Name"] == "tag-key"],
                         "the tag-key filter of EC2 is case-sensitive")
        self.assertEqual(called(calls, "delete_cluster"), [])
        self.assertEqual(called(calls, "delete_volume"), [{"VolumeId": "v-free"}])
        self.assertEqual(called(calls, "delete_network_interface"), [], "a cluster of a run still runs")
        # The group of the gone cluster named other goes too. Its interfaces
        # are still there in this pass, which is no failure.
        self.assertEqual(called(calls, "delete_security_group"), [{"GroupId": "sg-1"}, {"GroupId": "sg-3"}])
        self.assertIn("later pass: remove security group sg-3: DependencyViolation", LOG)
        self.assertEqual(run.failures, [])
        # The node of cpo-ci-1-eks is still listed in this pass, and the
        # cluster cpo-ci-2-eks runs.
        self.assertEqual(called(calls, "delete_schedule"),
                         [{"GroupName": "cpo-sandbox-eks", "Name": "cpo-ci-3-eks-delete-cluster"}])


if __name__ == "__main__":
    unittest.main()
