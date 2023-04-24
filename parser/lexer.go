package parser

import (
	"bytes"
	"errors"
)

var (
	ErrNestedExpression = errors.New("expressions cannot be nested")
	ErrInvalidEscape    = errors.New("invalid escape sequence")
	ErrOddIndentation   = errors.New("indentation spaces must be even")
)

type lexerState int

const (
	stateUnknown lexerState = iota
	stateIdent
	stateQuotedString
	stateMultilineString
	stateComment
)

type lexer struct {
	input []byte
	pos   position
	buf   bytes.Buffer

	indentation indentationScanner

	// Lexer state
	escapeLookbehind   byte
	lookahead          byte
	nextToken          *token
	state              lexerState
	expressionPopState lexerState // current state when expression was started
	tokenStartOffset   int        // pos.Offset of first character of the current token
	inExpression       bool
	inMapping          bool
}

func newLexer(input []byte) *lexer {
	return &lexer{input: input}
}

// NextToken reads from the Input and emits the next token.
// The token's type will be eofToken when the end of the file has been reached.
// A non-nil token pointer is returned for errors to indicate their position.
func (l *lexer) NextToken() (*token, error) {
	if l.nextToken != nil {
		tok := l.nextToken
		l.nextToken = nil
		l.state = stateIdent
		return tok, nil
	}

	tok, err := l.scan()
	if err != nil {
		return tok, err
	}
	return tok, nil
}

func (l *lexer) scan() (*token, error) {
	for _, b := range l.input[l.pos.Offset:] {
		l.pos.Offset++
		l.pos.Column++

		// Maintain one-char lookahead
		if l.pos.Offset < len(l.input) {
			l.lookahead = l.input[l.pos.Offset]
		}

		// The indentation state is kept separate from the main lexer for simplicity
		tok, skip, br, err := l.indentation.Scan(l.state, l.inExpression, &l.pos, b)
		if err != nil {
			return &token{Pos: l.pos}, err
		}
		if skip {
			continue
		}
		if br {
			break
		}
		if tok != nil {
			return tok, nil
		}

		tok, err = l.matchChar(b)
		if err != nil {
			return &token{Pos: l.pos}, err
		}
		if tok != nil {
			return tok, nil
		}
	}
	return l.buildToken(), nil
}

func (l *lexer) matchChar(b byte) (*token, error) {
	// Consume newline characters that directly follow the '|' of a multi-line string
	if l.state == stateMultilineString && l.tokenStartOffset+1 == l.pos.Offset && b == '\n' {
		l.reset()
		return nil, nil
	}

	// Consume any whitespace if not in a string
	if l.state != stateQuotedString && l.tokenStartOffset == 0 && (b == ' ' || b == '\t') {
		return nil, nil
	}

	// At this point we've reached the start of a statement (if not in one already)
	if l.tokenStartOffset == 0 {
		l.tokenStartOffset = l.pos.Offset
	}

	switch b {
	case '#':
		if l.interminableState() && !l.inExpression {
			l.state = stateComment
			l.tokenStartOffset = 0
			return nil, nil
		}

	case '-':
		// Dashes are just part of the current statement unless they are the first character
		if !l.atStateStart() || l.inExpression {
			break
		}

		return l.buffer(stateIdent, &token{
			Value: "-",
			Type:  listSeparatorToken,
			Pos:   l.pos,
		}), nil

	case '|':
		if l.state == stateQuotedString || !l.atStateStart() {
			break
		}

		if l.inExpression {
			return &token{
				Type:  pipeToken,
				Value: "|",
				Pos:   l.pos,
			}, nil
		}

		l.state = stateMultilineString
		l.buf.Reset() // we don't want the '|' char
		l.indentation.OpenMultilineString()
		return nil, nil

	case '\\':
		l.escapeLookbehind = b
		return nil, nil

	case '"':
		if l.escapeLookbehind == '\\' {
			l.escapeLookbehind = 0
			l.buf.WriteByte(b)
			return nil, nil
		}
		if l.state != stateQuotedString {
			l.tokenStartOffset = 0
			l.state = stateQuotedString
			return nil, nil
		}
		if l.state == stateQuotedString {
			return l.buildToken(), nil
		}

	case '{':
		if l.lookahead != '{' {
			return nil, nil
		}
		if l.inExpression {
			return nil, ErrNestedExpression
		}
		l.inExpression = true
		l.expressionPopState = l.state
		return l.buffer(stateIdent, &token{
			Type: openExpressionToken,
			Pos:  l.pos,
		}), nil

	case '}':
		if l.lookahead != '}' {
			return nil, nil
		}
		l.inExpression = false
		l.escapeLookbehind = 0
		l.pos.Offset++
		return l.buffer(l.expressionPopState, &token{
			Type: closeExpressionToken,
			Pos:  l.pos,
		}), nil

	case ':':
		if l.lookahead == '=' && l.inExpression && l.interminableState() {
			l.pos.Offset++
			return l.buffer(stateIdent, &token{
				Type:  assignmentOperatorToken,
				Value: ":=",
				Pos:   l.pos,
			}), nil
		}

		if l.atStateStart() {
			tok := &token{
				Value: ":",
				Type:  mappingSeparatorToken,
				Pos:   l.pos,
			}
			l.reset()
			return tok, nil
		}
		if l.interminableState() && !l.inExpression && !l.inMapping {
			l.pos.Offset--
			l.inMapping = true
			return l.buildToken(), nil
		}

	case ' ':
		if l.inExpression && l.buf.Len() == 0 {
			return nil, nil // discard leading spaces
		}
		if l.interminableState() && l.buf.Len() > 0 {
			return l.buildToken(), nil
		}

	case '\n':
		l.inMapping = false

		// Multi-line strings can (naturally) span multiple lines
		if l.state == stateMultilineString {
			l.reset()
			l.pos.Line++
			l.buf.WriteByte(b)
			return nil, nil
		}

		// Empty idents can occur when a newline directly follows another token
		if l.atStateStart() {
			l.reset()
			l.pos.Line++
			return nil, nil
		}
		return l.buildToken(), nil
	}

	// If we made it this far with an escape sequence in the lookbehind, it's invalid
	if l.escapeLookbehind == '\\' {
		return nil, ErrInvalidEscape
	}

	if l.state == stateUnknown {
		l.state = stateIdent
	}

	l.escapeLookbehind = 0
	l.buf.WriteByte(b)
	return nil, nil
}

// buffer allows a state transition (and associated token) to be returned to the caller _after_ the current token is flushed using buildToken().
func (l *lexer) buffer(nextState lexerState, nextToken *token) *token {
	if l.interminableState() && l.buf.Len() == 0 {
		l.reset()
		l.state = nextState
		return nextToken
	}

	if l.nextToken != nil {
		panic("cannot buffer multiple tokens")
	}
	l.nextToken = nextToken
	return l.buildToken()
}

// buildToken returns a token containing the current buffer. The token type is derived from the
// current lexer state.
func (l *lexer) buildToken() *token {
	// Interminable state + empty buffer means we've reached the end (otherwise this func wouldn't be called)
	if l.interminableState() && l.buf.Len() == 0 {
		return &token{Type: eofToken}
	}

	tok := &token{Value: l.buf.String(), Pos: l.pos}

	switch l.state {
	case stateIdent:
		tok.Type = identToken
	case stateQuotedString:
		tok.Type = quotedStringFragmentToken
	case stateMultilineString:
		tok.Type = multilineStringFragmentToken
		l.state = 0
	case stateComment:
		tok.Type = commentToken
	}

	l.reset()
	return tok
}

func (l *lexer) reset() {
	if l.state == stateMultilineString {
		return
	}
	l.tokenStartOffset = 0
	l.state = stateUnknown
	l.buf.Reset()
}

func (l *lexer) atStateStart() bool      { return l.pos.Offset == l.tokenStartOffset }
func (l *lexer) interminableState() bool { return l.state == stateUnknown || l.state == stateIdent }
