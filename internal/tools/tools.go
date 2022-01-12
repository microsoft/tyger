//go:build tools

// This is a workaround to use the oapi-codegen package and keep it in go.mod
// when it is only used for code generation.
package tools

import (
	_ "github.com/deepmap/oapi-codegen/pkg/codegen"
)
