#!/usr/bin/env bash
# aws-ebs needs nothing before its chart. It makes no AWS call, and the suite
# creates no volume, so no EBS CSI driver has to bind its StorageClass.
set -euo pipefail
