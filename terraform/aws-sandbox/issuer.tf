# STS reads the discovery document and the keys of the kind cluster of a run
# from a public HTTPS URL. Each run writes them under <run-id>/. Only those two
# documents are public, and every object expires after a day.
resource "aws_s3_bucket" "issuer" {
  bucket        = "cpo-sandbox-issuers-${data.aws_caller_identity.current.account_id}-${var.region}"
  force_destroy = true
}

resource "aws_s3_bucket_public_access_block" "issuer" {
  bucket                  = aws_s3_bucket.issuer.id
  block_public_acls       = true
  ignore_public_acls      = true
  block_public_policy     = false
  restrict_public_buckets = false
}

resource "aws_s3_bucket_ownership_controls" "issuer" {
  bucket = aws_s3_bucket.issuer.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "issuer" {
  bucket = aws_s3_bucket.issuer.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "issuer" {
  bucket = aws_s3_bucket.issuer.id
  rule {
    id     = "expire-runs"
    status = "Enabled"
    filter {}
    expiration {
      days = 1
    }
  }
}

data "aws_iam_policy_document" "issuer" {
  statement {
    actions   = ["s3:GetObject"]
    resources = local.issuer_documents
    principals {
      type        = "*"
      identifiers = ["*"]
    }
  }
  statement {
    effect    = "Deny"
    actions   = ["s3:*"]
    resources = [aws_s3_bucket.issuer.arn, "${aws_s3_bucket.issuer.arn}/*"]
    principals {
      type        = "*"
      identifiers = ["*"]
    }
    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }
}

resource "aws_s3_bucket_policy" "issuer" {
  bucket = aws_s3_bucket.issuer.id
  policy = data.aws_iam_policy_document.issuer.json

  # S3 refuses a public policy while the block of public policies is on.
  depends_on = [aws_s3_bucket_public_access_block.issuer]
}
