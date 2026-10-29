terraform {
  required_version = ">= 1.11"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.71"
    }
    github = {
      source  = "integrations/github"
      version = "~> 6.13"
    }
  }

  # Created by hand before the first apply. See README.md.
  backend "s3" {
    bucket       = "cpo-sandbox-tfstate-149561017949-eu-west-1"
    key          = "aws-sandbox/terraform.tfstate"
    region       = "eu-west-1"
    encrypt      = true
    use_lockfile = true
  }
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      managed-by = "hack/aws-sandbox/terraform"
    }
  }
}

# Reads its token from GITHUB_TOKEN.
provider "github" {
  owner = var.github_owner
}
