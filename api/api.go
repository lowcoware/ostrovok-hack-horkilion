// Package api embeds the external API contract, so services and tests check
// themselves against the same file clients are generated from.
package api

import _ "embed"

// OpenAPI is api/openapi.yaml.
//
//go:embed openapi.yaml
var OpenAPI []byte
