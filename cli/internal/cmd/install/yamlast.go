package install

import (
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/token"
)

// This is an incomplete implementation of merging YAML AST nodes that is meant to be used
// for "filling in" missing values in a YAML configuration file but preserving existing comments
// and formatting. It is incomplete in in that it assumes that src's mapping nodes are a superset of
// dst's mapping nodes, and that sequence nodes are of the same length in both src and dst.
func mergeAst(dst, src ast.Node) {
	switch src := src.(type) {
	case *ast.MappingNode:
		dst, ok := dst.(*ast.MappingNode)
		if !ok {
			return
		}

		dstKeyToValueMap := make(map[string]*ast.MappingValueNode)

		for _, item := range dst.Values {
			key := item.Key.String()
			dstKeyToValueMap[key] = item
		}

		for _, item := range src.Values {
			key := item.Key.String()
			if dstMappingValue, exists := dstKeyToValueMap[key]; exists {
				if dstMappingValue.Value == nil {
					dstMappingValue.Value = item.Value
				} else {
					dstComment := dstMappingValue.Value.GetComment()
					mergeAst(dstMappingValue.Value, item.Value)
					switch item.Value.Type() {
					case ast.MappingType, ast.SequenceType:
					default:
						dstMappingValue.Value = item.Value
						if dstComment != nil {
							dstMappingValue.Value.SetComment(dstComment)
						}
					}
				}
			} else {
				colDiff := dst.Values[0].Key.GetToken().Position.Column - item.Key.GetToken().Position.Column
				item.AddColumn(colDiff)
				dst.Values = append(dst.Values, item)
			}
		}

	case *ast.SequenceNode:
		dst, ok := dst.(*ast.SequenceNode)
		if !ok {
			return
		}
		if len(src.Values) != len(dst.Values) {
			panic("Merging sequence nodes with different lengths is not implemented")
		}

		for i, srcItem := range src.Values {
			mergeAst(dst.Values[i], srcItem)
		}
	}
}

type tokenFinderVisitor struct {
	target    *token.Token
	foundNode ast.Node
}

func (v *tokenFinderVisitor) Visit(node ast.Node) ast.Visitor {
	nodeToken := node.GetToken()
	if nodeToken == v.target || (nodeToken != nil && nodeToken.CharacterType == v.target.CharacterType && nodeToken.Value == v.target.Value && nodeToken.Position.Line == v.target.Position.Line && nodeToken.Position.Column == v.target.Position.Column) {

		v.foundNode = node
		return nil // Stop visiting if we found the target token
	}

	return v // Continue visiting other nodes
}
