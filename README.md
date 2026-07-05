# Terraform Provider for Dokku

A [Terraform](https://www.terraform.io) / [OpenTofu](https://opentofu.org) provider for provisioning resources on a [Dokku](https://dokku.com) PaaS instance. It drives the `dokku` CLI over SSH against Dokku's forced-command interface, so no HTTP API or agent needs to be installed on the target host beyond a working Dokku install and SSH access.

Built with the [Terraform Plugin Framework](https://github.com/hashicorp/terraform-plugin-framework).

## Requirements

- [Terraform](https://developer.hashicorp.com/terraform/downloads) >= 1.0 or [OpenTofu](https://opentofu.org/docs/intro/install/) >= 1.6
- [Go](https://golang.org/doc/install) >= 1.26
- A reachable Dokku host with SSH access via a registered public key

## Building the Provider

1. Clone the repository
1. Enter the repository directory
1. Build the provider using the Go `install` command:

```shell
go install
```

## Using the Provider

Configure the provider with your Dokku host and SSH credentials:

```hcl
provider "dokku" {
  host             = "dokku.example.com"
  ssh_user         = "dokku"
  private_key_path = "~/.ssh/id_dokku"
}
```
