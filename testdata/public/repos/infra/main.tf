module "network" {
  source = "./modules/network"
}

module "node_a" {
  source = "./modules/node"
  name   = "a"
}

module "node_b" {
  source = "./modules/node"
  name   = "b"
}

module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "6.0.1"
}

module "bucket" {
  source  = "terraform-aws-modules/s3-bucket/aws"
  version = "~> 5.4"
}
