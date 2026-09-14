variable "name" {
  type = string
}

variable "image" {
  type = string
  # renovate: datasource=docker depName=registry.acme.test/devops/images/node-agent versioning=semver
  default = "registry.acme.test/devops/images/node-agent:1.4.2"
}
