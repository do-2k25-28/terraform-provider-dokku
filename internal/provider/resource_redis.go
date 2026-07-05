// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import "github.com/hashicorp/terraform-plugin-framework/resource"

func NewRedisResource() resource.Resource {
	return &serviceResource{plugin: "redis"}
}

func NewRedisLinkResource() resource.Resource {
	return &serviceLinkResource{plugin: "redis"}
}
