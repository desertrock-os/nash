// Code generated by "stringer -type=NodeType"; DO NOT EDIT

package ast

import "fmt"

const _NodeType_name = "NodeSetenvNodeBlockNodeNameNodeAssignNodeExecAssignNodeImportexecBeginNodeCommandNodePipeNodeRedirectNodeFnInvexecEndexpressionBeginNodeStringExprNodeIntExprNodeVarExprNodeListExprNodeIndexExprNodeConcatExprexpressionEndNodeStringNodeRforkNodeRforkFlagsNodeIfNodeCommentNodeFnArgNodeFnDeclNodeReturnNodeBindFnNodeDumpNodeFor"

var _NodeType_index = [...]uint16{0, 10, 19, 27, 37, 51, 61, 70, 81, 89, 101, 110, 117, 132, 146, 157, 168, 180, 193, 207, 220, 230, 239, 253, 259, 270, 279, 289, 299, 309, 317, 324}

func (i NodeType) String() string {
	i -= 1
	if i < 0 || i >= NodeType(len(_NodeType_index)-1) {
		return fmt.Sprintf("NodeType(%d)", i+1)
	}
	return _NodeType_name[_NodeType_index[i]:_NodeType_index[i+1]]
}
