package parser

type token struct {
	Pos   position
	Type  tokenType
	Value string
	EOF   bool
}

type position struct {
	// Offset is the position of the cursor in bytes relative to the start of the input buffer.
	// In the context of each token, this marks the end of the token (since it has already been consumed).
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
	incrementIndentationToken
	decrementIndentationToken
	openExpressionToken
	closeExpressionToken
	commentToken
	pipeToken
	assignmentOperatorToken
)
