// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
	"github.com/stretchr/testify/require"
)

func TestFlatMerge(t *testing.T) {
	orig := `# This is a comment
a: abc # this is another comment

b: 123 # this is a comment

c: ""  # this is a comment

# This is a comment`

	expected := `# This is a comment
a: abc # this is another comment

b: 123 # this is a comment

c: 456 # this is a comment

# This is a comment`

	workingAst := parseAst(t, orig)
	parsed := astToValue(t, workingAst)
	parsed["c"] = 456
	mergeAst(workingAst, valueToAst(t, parsed))
	require.Equal(t, expected, workingAst.String())
}

func TestFlatMergeWithNewField(t *testing.T) {
	orig := `# This is a comment
a: abc # this is another comment

b: 123 # this is a comment

# This is a comment`

	expected := `# This is a comment
a: abc # this is another comment

b: 123 # this is a comment

# This is a comment
c: 456`

	workingAst := parseAst(t, orig)
	parsed := astToValue(t, workingAst)
	parsed["c"] = 456
	mergeAst(workingAst, valueToAst(t, parsed))
	require.Equal(t, expected, workingAst.String())
}

func TestFlatMergeWithSequence(t *testing.T) {
	orig := `a:
  - one: one # this is a comment
    two: two # this is another comment`

	expected := `a:
  - one: one # this is a comment
    two: two # this is another comment
    three: three`

	workingAst := parseAst(t, orig)
	parsed := astToValue(t, workingAst)
	parsed["a"].([]any)[0].(map[string]any)["three"] = "three"
	mergeAst(workingAst, valueToAst(t, parsed))
	require.Equal(t, expected, workingAst.String())
}

func TestMergeNotFromRoot(t *testing.T) {
	orig := `organizations:
  - name: org1
    api:
      accessControl:
        # This is a comment
        tenantId: 12345678-1234-1234-1234-123456789012
        apiAppId: ""`

	expected := `organizations:
  - name: org1
    api:
      accessControl:
        # This is a comment
        tenantId: 12345678-1234-1234-1234-123456789012
        apiAppId: 456789ab-4567-4567-4567-456789abcdef`

	workingAst := parseAst(t, orig)

	parsed := astToValue(t, workingAst)
	parsedAccessControl := parsed["organizations"].([]any)[0].(map[string]any)["api"].(map[string]any)["accessControl"].(map[string]any)
	parsedAccessControl["apiAppId"] = "456789ab-4567-4567-4567-456789abcdef"

	completedAccessControlAst := valueToAst(t, parsedAccessControl)

	path, err := yaml.PathString("$.organizations[0].api.accessControl")
	require.NoError(t, err)
	workingAccessControlAst, err := path.FilterNode(workingAst)
	require.NoError(t, err)
	mergeAst(workingAccessControlAst, completedAccessControlAst)

	require.Equal(t, expected, workingAst.String())

}

func parseAst(t *testing.T, input string) ast.Node {
	t.Helper()
	f, err := parser.ParseBytes([]byte(input), parser.ParseComments)
	require.NoError(t, err)
	return f.Docs[0].Body
}

func astToValue(t *testing.T, n ast.Node) map[string]any {
	t.Helper()
	res := map[string]any{}
	require.NoError(t, yaml.NodeToValue(n, &res))
	return res
}

func valueToAst(t *testing.T, value map[string]any) ast.Node {
	t.Helper()
	b, err := yaml.Marshal(value)
	require.NoError(t, err)
	return parseAst(t, string(b))
}
