// Package schema embeds the published JSON Schema for
// custos.io/v1alpha1, served at
// GET /api/v1/schemas/workflow/v1alpha1 (docs/workflows.md).
package schema

import _ "embed"

// V1Alpha1 is the workflow document JSON Schema.
//
//go:embed v1alpha1.json
var V1Alpha1 []byte
