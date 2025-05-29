// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package templatefunctions

import (
	"fmt"
	"reflect"
	"strings"
	"text/template"
	"unicode"

	"github.com/Masterminds/sprig/v3"
	"github.com/goccy/go-yaml"
)

func GetFuncMap() template.FuncMap {
	f := sprig.FuncMap()
	f["indent"] = Indent
	f["nindent"] = NIndent
	f["indentAfterFirst"] = IndentAfterFirst
	f["toYAML"] = ToYaml
	f["optionalField"] = OptionalField

	return f
}

// github.com/goccy/go-yaml gets confused when editing YAML files with comment blocks
// that are not aligned. So this function aligns consecutive comment lines.
// For example, this:
//
//	# helm:
//	  # traefik:
//	    # repoName:
//	    # repoUrl: not set if using `chartRef`
//
// Becomes:
//
//	# helm:
//	#   traefik:
//	#     repoName:
//	#     repoUrl: not set if using `chartRef`
func AlignConsecutiveCommentLinesByColumn(yamlString string) string {
	lines := strings.Split(yamlString, "\n")
	var result []string

	var commentBlock []string
	var minHashCol int

	flushBlock := func() {
		if len(commentBlock) == 0 {
			return
		}
		// Find the minimum column where '#' appears in the block
		minHashCol = -1
		for _, line := range commentBlock {
			hashIdx := strings.Index(line, "#")
			if hashIdx >= 0 && (minHashCol == -1 || hashIdx < minHashCol) {
				minHashCol = hashIdx
			}
		}
		// Align all '#' to minHashCol
		for _, line := range commentBlock {
			hashIdx := strings.Index(line, "#")
			afterHash := line[hashIdx+1:]
			aligned := strings.Repeat(" ", minHashCol) + "#" + strings.Repeat(" ", max(0, hashIdx-minHashCol)) + afterHash
			result = append(result, aligned)
		}
		commentBlock = nil
		minHashCol = 0
	}

	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		if strings.HasPrefix(trimmed, "#") {
			commentBlock = append(commentBlock, line)
		} else {
			flushBlock()
			result = append(result, line)
		}
	}
	flushBlock()
	return strings.Join(result, "\n")
}

// Indents each line of the given string by the specified number of spaces.
// Removes all trailing whitespace from each line
// Empty lines remain empty without any indentation
func Indent(spaces int, v string) string {
	pad := strings.Repeat(" ", spaces)
	lines := strings.Split(v, "\n")
	for i := range lines {
		line := strings.TrimRightFunc(lines[i], unicode.IsSpace)
		if len(line) > 0 {
			line = pad + line
		}

		lines[i] = line
	}

	return strings.Join(lines, "\n")
}

func NIndent(spaces int, v string) string {
	return "\n" + Indent(spaces, v)
}

func IndentAfterFirst(spaces int, v string) string {
	lines := strings.Split(v, "\n")
	if len(lines) <= 1 {
		return v
	}

	// Keep the first line as is, indent the rest
	indentedRest := Indent(spaces, strings.Join(lines[1:], "\n"))
	return lines[0] + "\n" + indentedRest
}

func ToYaml(v interface{}) string {
	b, err := yaml.MarshalWithOptions(v, yaml.IndentSequence(true), yaml.Indent(2))
	if err != nil {
		panic(err)
	}

	return string(b)
}

func OptionalField(name string, value any, comment string) string {
	// treat “empty” the same way Go’s templates do
	isEmpty := func(x any) bool {
		return x == nil ||
			x == "" ||
			x == false ||
			(reflect.ValueOf(x).Kind() == reflect.Slice &&
				reflect.ValueOf(x).Len() == 0)
	}

	if isEmpty(value) {
		if comment != "" {
			comment = " " + comment
		}

		return fmt.Sprintf("# %s:%s", name, comment)
	}

	if ptrValue := reflect.ValueOf(value); ptrValue.Kind() == reflect.Ptr && !ptrValue.IsNil() {
		value = ptrValue.Elem().Interface()
	}

	return fmt.Sprintf("%s: %v", name, value)
}
