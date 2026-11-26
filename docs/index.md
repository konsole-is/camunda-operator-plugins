# Plugins

Each plugin has one page, and so does each of the five controllers of the plugin `aws`. The page of `gateway-access`, of `rustfs`, and of each AWS controller tells an admin what it creates, what it needs before the install, and how to configure it in its chart. It also gives the parameters that a claim can change, the outputs that the controller publishes, the grants that it writes, what happens when a claim is deleted, and what to do for each reason of a failed condition. These pages have the same headings.

The five AWS controllers are in one chart, `aws`. The page [AWS plugins](plugins/aws.md) tells how to install that chart, and gives the values that its controllers share.

Plugin authors read [pluginkit](pluginkit.md), the shared library of every plugin.

## Pages

- [Gateway access](plugins/gateway-access.md): host names and Gateway API routes for clusters, Optimize, and management planes.
- [AWS plugins](plugins/aws.md): the one chart of the five AWS controllers below, its shared values, its IAM role, and the move from the five charts of older releases.
- [AWS identity](plugins/aws-identity.md): one IAM role for each cluster, with the grants of other plugins on it.
- [AWS Connectors role](plugins/aws-connectors-role.md): an IAM role for the Connectors of each cluster, with the policies that the admin allows.
- [AWS KMS key](plugins/aws-kms.md): one KMS key with rotation for each cluster, multi-Region on request.
- [AWS EBS encrypted volumes](plugins/aws-ebs.md): a StorageClass for each cluster that encrypts its EBS volumes with the key of the cluster.
- [AWS S3 bucket](plugins/aws-s3.md): one S3 bucket for the backups of each cluster, encrypted with the key of the cluster when it has one.
- [RustFS bucket](plugins/rustfs.md): a RustFS bucket and an access key for the backups of each cluster.
