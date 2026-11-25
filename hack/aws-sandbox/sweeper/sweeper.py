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
GONE = {"NoSuchEntity", "NotFoundException", "ResourceNotFoundException", "NoSuchBucket", "InvalidGroup.NotFound",
        "InvalidNetworkInterfaceID.NotFound", "InvalidVolume.NotFound", "InvalidInstanceID.NotFound"}
# EKS removes the interfaces of a deleted cluster some minutes later, and
# until then its security group is in use.
IN_USE = {"DependencyViolation"}


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


def names_run(value, rules):
    return bool(value) and value.startswith(rules["run_prefix"]) and len(value) > len(rules["run_prefix"])


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
    tags = {key.lower(): value for key, value in (cluster.get("tags") or {}).items()}
    tag = tags.get(rules["tag_key"])
    if not names_run(tag, rules):
        return False
    if not name.startswith(rules["run_prefix"]) and (cluster.get("resourcesVpcConfig") or {}).get("vpcId") != vpc:
        return False
    return matches(tag, cluster["createdAt"], now, rules["older_than_hours"]["eks_sweep"], rules)


def select_eks(tag, created, now, rules):
    # Each EKS kind is listed in the whole VPC or region, so the value must
    # name a run.
    return names_run(tag, rules) and older(created, now, rules["older_than_hours"]["eks_sweep"])


def schedule_owner(name):
    """Returns the run of a schedule <run>-delete-cluster or
    <run>-terminate-nodes."""
    for suffix in ("-delete-cluster", "-terminate-nodes"):
        if name.endswith(suffix):
            return name[: -len(suffix)]
    return name


def security_group_cluster(group):
    """Returns the cluster of an EKS cluster security group."""
    for tag in group.get("Tags") or []:
        if tag["Key"] == "aws:eks:cluster-name":
            return tag["Value"]
    return group["GroupName"][len("eks-cluster-sg-"):].rsplit("-", 1)[0]


def items(client, operation, key, **kwargs):
    for page in client.get_paginator(operation).paginate(**kwargs):
        yield from page.get(key, [])


class Sweep:
    """One pass of the sweep. clients(service, region) returns a boto3
    client. A failed step is recorded, and the pass goes on. log gets a line
    with a count for each listing, one for each removal with its reason, and
    one for each failure."""

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
        self.removed = 0

    def client(self, service, region=None):
        return self.clients(service, region or self.region)

    def step(self, what, call, *args, **kwargs):
        try:
            return call(*args, **kwargs)
        except Exception as err:  # noqa: BLE001
            if error_code(err) in GONE:
                return None
            if error_code(err) in IN_USE:
                self.log(f"later pass: {what}: {error_code(err)}")
                return None
            self.failures.append(f"{what}: {err}")
            self.log(f"failed: {what}: {err}")
            return None

    def listed(self, kind, count):
        self.log(f"listed {count} {kind}")

    def remove(self, kind, name, reason, call, *args, **kwargs):
        self.log(f"remove {kind} {name}: {reason}")
        self.removed += 1
        return self.step(f"remove {kind} {name}", call, *args, **kwargs)

    def age(self, tag, created):
        if isinstance(created, (int, float)):
            created = datetime.datetime.fromtimestamp(created, datetime.timezone.utc)
        hours = (self.now - created).total_seconds() / 3600
        return f"run {tag}, {hours:.1f} h old" if tag else f"{hours:.1f} h old"

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
        self.log(f"pass done: {self.removed} removals, {len(self.failures)} failures")
        return self.failures

    def run_instances(self, states):
        # The tag-key filter of EC2 is case-sensitive, and run_tag is not.
        filters = [{"Name": "vpc-id", "Values": [self.vpc]}, {"Name": "instance-state-name", "Values": states}]
        for reservation in items(self.client("ec2"), "describe_instances", "Reservations", Filters=filters):
            yield from reservation["Instances"]

    def instances(self):
        instances = list(self.run_instances(["pending", "running", "shutting-down", "stopping", "stopped"]))
        self.listed("EC2 instances in the VPC", len(instances))
        ids = []
        for instance in instances:
            tag = run_tag(instance.get("Tags"), self.rules)
            if select_eks(tag, instance["LaunchTime"], self.now, self.rules):
                self.log(f"remove EC2 instance {instance['InstanceId']}: {self.age(tag, instance['LaunchTime'])}")
                self.removed += 1
                ids.append(instance["InstanceId"])
        if ids:
            self.step("terminate " + " ".join(ids), self.client("ec2").terminate_instances, InstanceIds=ids)

    def clusters(self):
        eks = self.client("eks")
        names = list(items(eks, "list_clusters", "clusters"))
        self.listed("EKS clusters", len(names))
        for name in names:
            described = self.step("describe cluster " + name, eks.describe_cluster, name=name)
            if not described:
                continue
            cluster = described["cluster"]
            # A cluster in CREATING cannot be deleted yet.
            if select_cluster(name, cluster, self.vpc, self.now, self.rules) and cluster["status"] not in (
                    "CREATING", "DELETING"):
                tags = {k.lower(): v for k, v in (cluster.get("tags") or {}).items()}
                self.remove("EKS cluster", name, self.age(tags[self.rules["tag_key"]], cluster["createdAt"]),
                            eks.delete_cluster, name=name)

    def volumes(self):
        ec2 = self.client("ec2")
        volumes = list(items(ec2, "describe_volumes", "Volumes"))
        self.listed("EBS volumes", len(volumes))
        for volume in volumes:
            tag = run_tag(volume.get("Tags"), self.rules)
            if volume["State"] == "available" and select_eks(tag, volume["CreateTime"], self.now, self.rules):
                self.remove("EBS volume", volume["VolumeId"], self.age(tag, volume["CreateTime"]), ec2.delete_volume,
                            VolumeId=volume["VolumeId"])

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
        # that is gone, whatever the name of that cluster.
        if not clusters:
            interfaces = list(items(ec2, "describe_network_interfaces", "NetworkInterfaces", Filters=vpc))
            self.listed("network interfaces in the VPC", len(interfaces))
            for interface in interfaces:
                if interface["Status"] == "available":
                    self.remove("network interface", interface["NetworkInterfaceId"], "available, and no cluster runs",
                                ec2.delete_network_interface, NetworkInterfaceId=interface["NetworkInterfaceId"])
        groups = list(items(ec2, "describe_security_groups", "SecurityGroups",
                            Filters=vpc + [{"Name": "group-name", "Values": ["eks-cluster-sg-*"]}]))
        self.listed("EKS cluster security groups in the VPC", len(groups))
        for group in groups:
            owner = security_group_cluster(group)
            if owner not in clusters:
                self.remove("security group", group["GroupId"], f"its cluster {owner} is gone",
                            ec2.delete_security_group, GroupId=group["GroupId"])

    def images(self):
        ecr = self.client("ecr")
        for repo in self.repositories:
            images = list(items(ecr, "describe_images", "imageDetails", repositoryName=repo))
            self.listed(f"images of {repo}", len(images))
            ids = []
            for image in images:
                for tag in image.get("imageTags") or []:
                    if tag.startswith(self.rules["run_prefix"]) and select_eks(
                            tag, image["imagePushedAt"], self.now, self.rules):
                        self.log(f"remove image {repo}:{tag}: {self.age(tag, image['imagePushedAt'])}")
                        self.removed += 1
                        ids.append({"imageTag": tag})
            if not ids:
                continue
            result = self.step("remove images of " + repo, ecr.batch_delete_image, repositoryName=repo, imageIds=ids)
            for failure in (result or {}).get("failures") or []:
                self.failures.append(f"remove image of {repo}: {failure}")
                self.log(f"failed: remove image of {repo}: {failure}")

    def log_groups(self):
        logs = self.client("logs")
        groups = list(items(logs, "describe_log_groups", "logGroups",
                            logGroupNamePrefix="/aws/eks/" + self.rules["run_prefix"]))
        self.listed("EKS log groups", len(groups))
        for group in groups:
            run = group["logGroupName"].split("/")[3]
            if select_eks(run, group["creationTime"] / 1000, self.now, self.rules):
                self.remove("log group", group["logGroupName"], self.age(run, group["creationTime"] / 1000),
                            logs.delete_log_group, logGroupName=group["logGroupName"])

    def schedules(self):
        scheduler = self.client("scheduler")
        clusters, live = self.remaining()
        schedules = list(items(scheduler, "list_schedules", "Schedules", GroupName=self.group,
                               NamePrefix=self.rules["run_prefix"]))
        self.listed("schedules of runs", len(schedules))
        for schedule in schedules:
            owner = schedule_owner(schedule["Name"])
            # The schedules end a run whose cleanup did not finish, so they
            # stay while its cluster or its node is there.
            if owner not in clusters and owner not in live and select_eks(
                    owner, schedule["CreationDate"], self.now, self.rules):
                self.remove("schedule", schedule["Name"], self.age(owner, schedule["CreationDate"]) + ", no cluster",
                            scheduler.delete_schedule, GroupName=self.group, Name=schedule["Name"])

    def roles(self, account):
        iam = self.client("iam")
        roles = [r for r in items(iam, "list_roles", "Roles") if r["RoleName"].startswith(self.rules["run_prefix"])]
        self.listed("roles of runs", len(roles))
        for role in roles:
            tags = self.step("read the tags of role " + role["RoleName"], iam.list_role_tags,
                             RoleName=role["RoleName"])
            if tags is None:
                continue
            tag = run_tag(tags.get("Tags"), self.rules)
            reason = select_role(role, tag, account, self.now, self.rules)
            if reason:
                if reason.startswith("run "):
                    reason = self.age(tag, role["CreateDate"])
                self.remove("role", role["RoleName"], reason, self.delete_role, iam, role["RoleName"])

    @staticmethod
    def delete_role(iam, name):
        for policy in iam.list_role_policies(RoleName=name).get("PolicyNames", []):
            iam.delete_role_policy(RoleName=name, PolicyName=policy)
        for policy in iam.list_attached_role_policies(RoleName=name).get("AttachedPolicies", []):
            iam.detach_role_policy(RoleName=name, PolicyArn=policy["PolicyArn"])
        for profile in iam.list_instance_profiles_for_role(RoleName=name).get("InstanceProfiles", []):
            iam.remove_role_from_instance_profile(InstanceProfileName=profile["InstanceProfileName"], RoleName=name)
        iam.delete_role(RoleName=name)

    def policies(self):
        iam = self.client("iam")
        policies = [p for p in items(iam, "list_policies", "Policies", Scope="Local")
                    if p["PolicyName"].startswith(self.rules["run_prefix"])]
        self.listed("policies of runs", len(policies))
        for policy in policies:
            tags = self.step("read the tags of policy " + policy["Arn"], iam.list_policy_tags, PolicyArn=policy["Arn"])
            if tags is None:
                continue
            tag = run_tag(tags.get("Tags"), self.rules)
            if matches(tag, policy["CreateDate"], self.now, self.rules["older_than_hours"]["sweep"], self.rules):
                self.remove("policy", policy["Arn"], self.age(tag, policy["CreateDate"]), self.delete_policy, iam,
                            policy["Arn"])

    @staticmethod
    def delete_policy(iam, arn):
        for version in iam.list_policy_versions(PolicyArn=arn).get("Versions", []):
            if not version["IsDefaultVersion"]:
                iam.delete_policy_version(PolicyArn=arn, VersionId=version["VersionId"])
        iam.delete_policy(PolicyArn=arn)

    def providers(self):
        iam = self.client("iam")
        providers = iam.list_open_id_connect_providers()["OpenIDConnectProviderList"]
        self.listed("OIDC providers", len(providers))
        for entry in providers:
            try:
                provider = iam.get_open_id_connect_provider(OpenIDConnectProviderArn=entry["Arn"])
            except Exception as err:  # noqa: BLE001
                # The role reads only the providers of the issuer bucket.
                if error_code(err) not in GONE | {"AccessDenied"}:
                    self.failures.append(f"read OIDC provider {entry['Arn']}: {err}")
                    self.log(f"failed: read OIDC provider {entry['Arn']}: {err}")
                continue
            tag = run_tag(provider.get("Tags"), self.rules)
            if matches(tag, provider["CreateDate"], self.now, self.rules["older_than_hours"]["sweep"], self.rules):
                self.remove("OIDC provider", entry["Arn"], self.age(tag, provider["CreateDate"]),
                            iam.delete_open_id_connect_provider, OpenIDConnectProviderArn=entry["Arn"])

    def buckets(self):
        s3 = self.client("s3")
        buckets = [b for b in s3.list_buckets().get("Buckets", []) if b["Name"].startswith(self.rules["run_prefix"])]
        self.listed("buckets of runs", len(buckets))
        for bucket in buckets:
            if select_bucket(bucket["Name"], bucket["CreationDate"], self.now, self.rules):
                self.remove("bucket", bucket["Name"], self.age(None, bucket["CreationDate"]), self.delete_bucket, s3,
                            bucket["Name"])

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
        self.log(f"later pass: bucket {name} still has objects")

    def keys(self):
        for region in [r for r in (self.region, self.replica_region) if r]:
            kms = self.client("kms", region)
            mappings = list(items(self.client("resourcegroupstaggingapi", region), "get_resources",
                                  "ResourceTagMappingList", ResourceTypeFilters=["kms:key"]))
            self.listed(f"KMS keys with tags in {region}", len(mappings))
            for mapping in mappings:
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
                    self.remove(f"KMS key in {region}", key, self.age(tag, metadata["CreationDate"]), self.delete_key,
                                kms, key)

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
