terraform {
  required_version = ">= 1.9.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.11"
    }
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = ">= 5.9, < 6"
    }
    gitlab = {
      source  = "gitlabhq/gitlab"
      version = "18.4.0"
    }
    random = {
      source = "hashicorp/random"
    }
    tls = {
      source = "hashicorp/tls"
    }
    null = {
      source = "hashicorp/null"
    }
  }
}
