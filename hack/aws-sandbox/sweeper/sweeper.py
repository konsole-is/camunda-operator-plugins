"""The scheduled sweep of the AWS sandbox, as a Lambda function.

It removes what CI runs left, with the rules of hack/aws-sandbox/sweep.sh
--older-than, hack/aws-sandbox/eks-sweep.sh --older-than, and
hack/aws-sandbox/trust.jq.

A pass waits for nothing. A resource that is not ready yet, such as a volume
that is still in use, goes in a later pass.
"""

import datetime
import json
import os
import time

RULES_FILE = "sweep-rules.json"
BUCKET_SECONDS = 60
# A resource that another sweep deleted since the listing is no failure.
GONE = {"NoSuchEntity", "NotFoundException", "ResourceNotFoundException", "NoSuchBucket"}


def load_rules(path=None):
    """Returns sweep-rules.json: next to this file in the function, or one
    directory up in the repository."""
    here = os.path.dirname(os.path.abspath(__file__))
    for candidate in (path, os.path.join(here, RULES_FILE), os.path.join(here, "..", RULES_FILE)):
        if candidate and os.path.exists(candidate):
            with open(candidate) as f:
                return json.load(f)
    raise FileNotFoundError(RULES_FILE)


def error_code(err):
    """Returns the AWS error code of a botocore ClientError, or ""."""
    return getattr(err, "response", {}).get("Error", {}).get("Code", "")


def run_tag(tags, rules):
    """Returns the value of the run tag, in any case of its key, or None."""
    for tag in tags or []:
        if tag.get("Key", tag.get("TagKey", "")).lower() == rules["tag_key"]:
            return tag.get("Value", tag.get("TagValue", ""))
    return None


def has_run(value, rules):
    return value is not None and value not in rules["ignored_values"]


def older(created, now, hours):
    """Tells if created, a datetime or epoch seconds, lies more than hours
    before now."""
    if isinstance(created, (int, float)):
        created = datetime.datetime.fromtimestamp(created, datetime.timezone.utc)
    return created < now - datetime.timedelta(hours=hours)


def matches(value, created, now, hours, rules):
    return has_run(value, rules) and older(created, now, hours)


def foreign_trust(document, account, rules):
    """The rule of foreign of trust.jq."""
    if isinstance(document, str):
        document = json.loads(document)
    statements = (document or {}).get("Statement") or []
    if isinstance(statements, dict):
        statements = [statements]

    def own(arn):
        parts = arn.split(":")
        return (len(parts) > 4 and parts[0] == "arn" and parts[1].startswith("aws")
                and parts[2] in ("iam", "sts") and parts[4] == account)

    def trusted(kind, value):
        if kind == "AWS":
            return own(value) or value == account
        if kind == "Federated":
            return own(value) and ":oidc-provider/" in value and not any(
                value.endswith(":oidc-provider/" + host) for host in rules["untrusted_provider_hosts"])
        if kind == "Service":
            return value in rules["trusted_services"]
        return False

    for statement in statements:
        if statement.get("Effect") != "Allow":
            continue
        principal = statement.get("Principal")
        if "NotPrincipal" in statement or not isinstance(principal, dict):
            return True
        for kind, values in principal.items():
            if not all(trusted(kind, v) for v in (values if isinstance(values, list) else [values])):
                return True
    return False


def select_role(role, tag, account, now, rules):
    """Returns why the sweep removes a role, or None to keep it. A cpo-ci-*
    role with a trust of no run goes at any age."""
    if not role["RoleName"].startswith(rules["run_prefix"]):
        return None
    if foreign_trust(role.get("AssumeRolePolicyDocument"), account, rules):
        return "a trust of no run"
    if matches(tag, role["CreateDate"], now, rules["older_than_hours"]["sweep"], rules):
        return "run " + tag
    return None


def select_bucket(name, created, now, rules):
    # sweep.sh removes an old cpo-ci-* bucket with the tag of a run and one
    # without a run value, so the tag does not decide.
    return name.startswith(rules["run_prefix"]) and older(created, now, rules["older_than_hours"]["sweep"])


def select_cluster(name, cluster, vpc, now, rules):
    """The cluster rule of eks-sweep.sh: a run tag that starts with the run
    prefix, and a cpo-ci- name or the VPC of the runs."""
    tag = (cluster.get("tags") or {}).get(rules["tag_key"])
    if not tag or not tag.startswith(rules["run_prefix"]):
        return False
    if not name.startswith(rules["run_prefix"]) and (cluster.get("resourcesVpcConfig") or {}).get("vpcId") != vpc:
        return False
    return matches(tag, cluster["createdAt"], now, rules["older_than_hours"]["eks_sweep"], rules)


def select_eks(tag, created, now, rules):
    return matches(tag, created, now, rules["older_than_hours"]["eks_sweep"], rules)


def schedule_owner(name):
    """Returns the run of a schedule <run>-delete-cluster or
    <run>-terminate-nodes."""
    for suffix in ("-delete-cluster", "-terminate-nodes"):
        if name.endswith(suffix):
            return name[: -len(suffix)]
    return name


def security_group_cluster(group_name):
    """Returns the cluster of the security group eks-cluster-sg-<cluster>-<number>."""
    return group_name[len("eks-cluster-sg-"):].rsplit("-", 1)[0]


def items(client, operation, key, **kwargs):
    for page in client.get_paginator(operation).paginate(**kwargs):
        yield from page.get(key, [])


class Sweep:
    """One pass of the sweep. clients(service, region) returns a boto3
    client. A failed step is recorded, and the pass goes on."""

    def __init__(self, clients, rules, env, now=None, log=print):
        self.clients = clients
        self.rules = rules
        self.region = env["AWS_REGION"]
        self.replica_region = env.get("AWS_KMS_REPLICA_REGION", "")
        self.vpc = env["AWS_EKS_VPC_ID"]
        self.repositories = [r for r in env.get("AWS_EKS_REPOSITORIES", "").split(",") if r]
        self.group = env["AWS_EKS_SCHEDULE_GROUP"]
        self.now = now or datetime.datetime.now(datetime.timezone.utc)
        self.log = log
        self.failures = []

    def client(self, service, region=None):
        return self.clients(service, region or self.region)

    def step(self, what, call, *args, **kwargs):
        try:
            return call(*args, **kwargs)
        except Exception as err:  # noqa: BLE001
            if error_code(err) in GONE:
                return None
            self.failures.append(f"{what}: {err}")
            self.log(f"failed: {what}: {err}")
            return None

    def run(self):
        """Runs each kind and returns the failures."""
        account = self.client("sts").get_caller_identity()["Account"]
        # The order of eks-sweep.sh and then sweep.sh.
        for name, part in (
            ("EC2 instances", self.instances), ("EKS clusters", self.clusters), ("EBS volumes", self.volumes),
            ("network", self.network), ("images", self.images), ("log groups", self.log_groups),
            ("schedules", self.schedules), ("roles", lambda: self.roles(account)), ("policies", self.policies),
            ("OIDC providers", self.providers), ("buckets", self.buckets), ("KMS keys", self.keys),
        ):
            self.step("sweep " + name, part)
        return self.failures

    def run_instances(self, states):
        filters = [{"Name": "vpc-id", "Values": [self.vpc]}, {"Name": "tag-key", "Values": [self.rules["tag_key"]]},
                   {"Name": "instance-state-name", "Values": states}]
        for reservation in items(self.client("ec2"), "describe_instances", "Reservations", Filters=filters):
            yield from reservation["Instances"]

    def instances(self):
        ids = []
        for instance in self.run_instances(["pending", "running", "shutting-down", "stopping", "stopped"]):
            tag = run_tag(instance.get("Tags"), self.rules)
            if select_eks(tag, instance["LaunchTime"], self.now, self.rules):
                self.log(f"EC2 instance {instance['InstanceId']} ({tag})")
                ids.append(instance["InstanceId"])
        if ids:
            self.step("terminate " + " ".join(ids), self.client("ec2").terminate_instances, InstanceIds=ids)

    def clusters(self):
        eks = self.client("eks")
        for name in items(eks, "list_clusters", "clusters"):
            described = self.step("describe cluster " + name, eks.describe_cluster, name=name)
            if not described:
                continue
            cluster = described["cluster"]
            # A cluster in CREATING cannot be deleted yet.
            if select_cluster(name, cluster, self.vpc, self.now, self.rules) and cluster["status"] not in (
                    "CREATING", "DELETING"):
                self.log(f"EKS cluster {name} ({cluster['status']})")
                self.step("delete cluster " + name, eks.delete_cluster, name=name)

    def volumes(self):
        ec2 = self.client("ec2")
        for volume in items(ec2, "describe_volumes", "Volumes",
                            Filters=[{"Name": "tag-key", "Values": [self.rules["tag_key"]]}]):
            tag = run_tag(volume.get("Tags"), self.rules)
            if volume["State"] == "available" and select_eks(tag, volume["CreateTime"], self.now, self.rules):
                self.log(f"EBS volume {volume['VolumeId']} ({tag})")
                self.step("delete volume " + volume["VolumeId"], ec2.delete_volume, VolumeId=volume["VolumeId"])

    def remaining(self):
        """Returns the clusters of the region and the runs of the instances
        of the VPC that are not terminated."""
        clusters = set(items(self.client("eks"), "list_clusters", "clusters"))
        states = ["pending", "running", "shutting-down", "stopping", "stopped"]
        live = {run_tag(i.get("Tags"), self.rules) for i in self.run_instances(states)}
        return clusters, live

    def network(self):
        ec2 = self.client("ec2")
        clusters, _ = self.remaining()
        vpc = [{"Name": "vpc-id", "Values": [self.vpc]}]
        # The VPC holds only runs, so an available interface is a leftover
        # once no cluster runs, and so is the security group of a cluster
        # that is gone.
        if not clusters:
            for interface in items(ec2, "describe_network_interfaces", "NetworkInterfaces", Filters=vpc):
                if interface["Status"] == "available":
                    self.log(f"network interface {interface['NetworkInterfaceId']}")
                    self.step("delete network interface " + interface["NetworkInterfaceId"],
                              ec2.delete_network_interface, NetworkInterfaceId=interface["NetworkInterfaceId"])
        groups = vpc + [{"Name": "group-name", "Values": ["eks-cluster-sg-" + self.rules["run_prefix"] + "*"]}]
        for group in items(ec2, "describe_security_groups", "SecurityGroups", Filters=groups):
            if security_group_cluster(group["GroupName"]) not in clusters:
                self.log(f"security group {group['GroupId']} ({group['GroupName']})")
                self.step("delete security group " + group["GroupId"], ec2.delete_security_group,
                          GroupId=group["GroupId"])

    def images(self):
        ecr = self.client("ecr")
        for repo in self.repositories:
            ids = [{"imageTag": tag}
                   for image in items(ecr, "describe_images", "imageDetails", repositoryName=repo)
                   for tag in image.get("imageTags") or []
                   if tag.startswith(self.rules["run_prefix"])
                   and select_eks(tag, image["imagePushedAt"], self.now, self.rules)]
            if not ids:
                continue
            self.log(f"images of {repo}: {', '.join(i['imageTag'] for i in ids)}")
            result = self.step("delete images of " + repo, ecr.batch_delete_image, repositoryName=repo, imageIds=ids)
            for failure in (result or {}).get("failures") or []:
                self.failures.append(f"delete image of {repo}: {failure}")

    def log_groups(self):
        logs = self.client("logs")
        for group in items(logs, "describe_log_groups", "logGroups",
                           logGroupNamePrefix="/aws/eks/" + self.rules["run_prefix"]):
            run = group["logGroupName"].split("/")[3]
            if select_eks(run, group["creationTime"] / 1000, self.now, self.rules):
                self.log(f"log group {group['logGroupName']}")
                self.step("delete log group " + group["logGroupName"], logs.delete_log_group,
                          logGroupName=group["logGroupName"])

    def schedules(self):
        scheduler = self.client("scheduler")
        clusters, live = self.remaining()
        for schedule in items(scheduler, "list_schedules", "Schedules", GroupName=self.group,
                              NamePrefix=self.rules["run_prefix"]):
            owner = schedule_owner(schedule["Name"])
            # The schedules end a run whose cleanup did not finish, so they
            # stay while its cluster or its node is there.
            if owner not in clusters and owner not in live and select_eks(
                    owner, schedule["CreationDate"], self.now, self.rules):
                self.log(f"schedule {schedule['Name']}")
                self.step("delete schedule " + schedule["Name"], scheduler.delete_schedule, GroupName=self.group,
                          Name=schedule["Name"])

    def roles(self, account):
        iam = self.client("iam")
        for role in items(iam, "list_roles", "Roles"):
            if not role["RoleName"].startswith(self.rules["run_prefix"]):
                continue
            tags = self.step("read the tags of role " + role["RoleName"], iam.list_role_tags,
                             RoleName=role["RoleName"])
            if tags is None:
                continue
            reason = select_role(role, run_tag(tags.get("Tags"), self.rules), account, self.now, self.rules)
            if reason:
                self.log(f"role {role['RoleName']} ({reason})")
                self.step("delete role " + role["RoleName"], self.delete_role, iam, role["RoleName"])

    @staticmethod
    def delete_role(iam, name):
        for policy in iam.list_role_policies(RoleName=name)["PolicyNames"]:
            iam.delete_role_policy(RoleName=name, PolicyName=policy)
        for policy in iam.list_attached_role_policies(RoleName=name)["AttachedPolicies"]:
            iam.detach_role_policy(RoleName=name, PolicyArn=policy["PolicyArn"])
        iam.delete_role(RoleName=name)

    def policies(self):
        iam = self.client("iam")
        for policy in items(iam, "list_policies", "Policies", Scope="Local"):
            if not policy["PolicyName"].startswith(self.rules["run_prefix"]):
                continue
            tags = self.step("read the tags of policy " + policy["Arn"], iam.list_policy_tags, PolicyArn=policy["Arn"])
            if tags is None:
                continue
            tag = run_tag(tags.get("Tags"), self.rules)
            if matches(tag, policy["CreateDate"], self.now, self.rules["older_than_hours"]["sweep"], self.rules):
                self.log(f"policy {policy['Arn']} ({tag})")
                self.step("delete policy " + policy["Arn"], self.delete_policy, iam, policy["Arn"])

    @staticmethod
    def delete_policy(iam, arn):
        for version in iam.list_policy_versions(PolicyArn=arn)["Versions"]:
            if not version["IsDefaultVersion"]:
                iam.delete_policy_version(PolicyArn=arn, VersionId=version["VersionId"])
        iam.delete_policy(PolicyArn=arn)

    def providers(self):
        iam = self.client("iam")
        for entry in iam.list_open_id_connect_providers()["OpenIDConnectProviderList"]:
            try:
                provider = iam.get_open_id_connect_provider(OpenIDConnectProviderArn=entry["Arn"])
            except Exception:  # noqa: BLE001 - the role reads only the providers of the issuer bucket
                continue
            tag = run_tag(provider.get("Tags"), self.rules)
            if matches(tag, provider["CreateDate"], self.now, self.rules["older_than_hours"]["sweep"], self.rules):
                self.log(f"OIDC provider {entry['Arn']} ({tag})")
                self.step("delete OIDC provider " + entry["Arn"], iam.delete_open_id_connect_provider,
                          OpenIDConnectProviderArn=entry["Arn"])

    def buckets(self):
        s3 = self.client("s3")
        for bucket in s3.list_buckets().get("Buckets", []):
            if select_bucket(bucket["Name"], bucket["CreationDate"], self.now, self.rules):
                self.log(f"bucket {bucket['Name']}")
                self.step("delete bucket " + bucket["Name"], self.delete_bucket, s3, bucket["Name"])

    def delete_bucket(self, s3, name):
        deadline = time.monotonic() + BUCKET_SECONDS
        while time.monotonic() < deadline:
            page = s3.list_object_versions(Bucket=name, MaxKeys=500)
            objects = [{"Key": o["Key"], "VersionId": o["VersionId"]}
                       for o in page.get("Versions", []) + page.get("DeleteMarkers", [])]
            if not objects:
                s3.delete_bucket(Bucket=name)
                return
            result = s3.delete_objects(Bucket=name, Delete={"Objects": objects, "Quiet": True})
            if result.get("Errors"):
                raise RuntimeError(f"{len(result['Errors'])} objects not deleted, first: {result['Errors'][0]}")
        self.log(f"bucket {name}: objects are left for the next pass")

    def keys(self):
        for region in [r for r in (self.region, self.replica_region) if r]:
            kms = self.client("kms", region)
            for mapping in items(self.client("resourcegroupstaggingapi", region), "get_resources",
                                 "ResourceTagMappingList", ResourceTypeFilters=["kms:key"]):
                tag = run_tag(mapping.get("Tags"), self.rules)
                if not has_run(tag, self.rules):
                    continue
                key = mapping["ResourceARN"].rsplit("/", 1)[1]
                # The tagging index can still list a key that KMS deleted.
                described = self.step("describe key " + key, kms.describe_key, KeyId=key)
                if not described:
                    continue
                metadata = described["KeyMetadata"]
                if metadata["KeyState"] in ("PendingDeletion", "PendingReplicaDeletion"):
                    continue
                if matches(tag, metadata["CreationDate"], self.now, self.rules["older_than_hours"]["sweep"],
                           self.rules):
                    self.log(f"KMS key {key} in {region} ({tag})")
                    self.step("delete key " + key, self.delete_key, kms, key)

    @staticmethod
    def delete_key(kms, key):
        for alias in kms.list_aliases(KeyId=key).get("Aliases", []):
            kms.delete_alias(AliasName=alias["AliasName"])
        kms.schedule_key_deletion(KeyId=key, PendingWindowInDays=7)


def handler(event, context):
    """The Lambda entry point. It fails the invocation when a step failed."""
    import boto3  # The tests do not need it.

    cache = {}

    def clients(service, region):
        if (service, region) not in cache:
            cache[(service, region)] = boto3.client(service, region_name=region)
        return cache[(service, region)]

    failures = Sweep(clients, load_rules(), os.environ).run()
    # The alarm on the Errors metric fires only on a failed invocation.
    if failures:
        raise RuntimeError(f"{len(failures)} steps failed: " + "; ".join(failures[:10]))
    return {"failures": 0}
