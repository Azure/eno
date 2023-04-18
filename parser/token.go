package parser

type token struct {
	Pos   position
	Type  tokenType
	Value string
	EOF   bool
}

type position struct {
	Offset int
	Line   int
	Column int
}

type tokenType int

const (
	mappingSeparatorToken tokenType = iota
	listSeparatorToken
	identToken
	quotedStringFragmentToken
	multilineStringFragmentToken
	indentToken
	unindentToken
	openExpressionToken
	closeExpressionToken
	commentToken
	pipeToken
	assignmentOperatorToken
)
