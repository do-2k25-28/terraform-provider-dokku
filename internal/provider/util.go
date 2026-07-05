// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
)

// parseJSONField extracts a single field from a flat JSON object as a
// string, returning "" if the document doesn't parse or the key is absent.
// Values are decoded via map[string]interface{} rather than
// map[string]string because not all Dokku commands' --format json output
// is string-typed (e.g. storage:info emits schema_version as a JSON
// number); unmarshaling straight into map[string]string would fail the
// whole document on the first non-string value.
func parseJSONField(doc, key string) string {
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// parseJSONList decodes a JSON array of flat objects, as returned by
// commands like `network:list --format json`, stringifying each value.
func parseJSONList(doc string) ([]map[string]string, error) {
	var raw []map[string]interface{}
	if err := json.Unmarshal([]byte(doc), &raw); err != nil {
		return nil, err
	}
	out := make([]map[string]string, len(raw))
	for i, obj := range raw {
		m := make(map[string]string, len(obj))
		for k, v := range obj {
			if s, ok := v.(string); ok {
				m[k] = s
			} else {
				m[k] = fmt.Sprint(v)
			}
		}
		out[i] = m
	}
	return out, nil
}
