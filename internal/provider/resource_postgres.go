// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import "github.com/hashicorp/terraform-plugin-framework/resource"

func NewPostgresResource() resource.Resource {
	return &serviceResource{plugin: "postgres"}
}

func NewPostgresLinkResource() resource.Resource {
	return &serviceLinkResource{plugin: "postgres"}
}
