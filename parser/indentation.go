package parser

type indentationScanner struct {
	previousLine    int
	currentLine     int
	multilineRef    int
	pastIndentation bool
}

func (i *indentationScanner) Scan(state lexerState, inExpr bool, pos *position, b byte) (*token, bool /* skip */, bool /* break */, error) {
	if inExpr && b == '\n' {
		// Ignore newlines in expressions so they can span multiple lines if needed
		return nil, false, false, nil
	}

	if i.pastIndentation {
		if b != '\n' {
			// Ignore characters that are not applicable
			return nil, false, false, nil
		}

		// Reset the per-line state at each newline character
		i.currentLine = 0
		i.pastIndentation = false
		return nil, false, false, nil
	}

	// In multi-line strings, don't consume more indentation than the reference amount e.g. indentation of first line.
	// Otherwise we will chomp any leading spaces within the string.
	if state != stateMultilineString || i.currentLine < i.multilineRef {
		if b == ' ' {
			i.currentLine++
			return nil, true, false, nil
		}
		if b == '\t' {
			i.currentLine += 2
			return nil, true, false, nil
		}
	}

	// Indentation must be increments of two spaces
	if i.currentLine%2 != 0 {
		return nil, false, false, ErrOddIndentation
	}

	// If we made it this far into the func then this line's indentation is complete
	i.pastIndentation = true

	delta := i.currentLine - i.previousLine
	i.previousLine = i.currentLine

	// The first line serves as a reference for subsequent lines.
	// In other words, all future indent/unindent tokens are relative to this line's indentation.
	if pos.Line <= 0 {
		return nil, false, false, nil
	}

	// Emit indent/unindent tokens for changes in indentation.
	//
	// Indent tokens are ignored in multiline strings since any extra whitespace is included in the string.
	// In this case unindent tokens are also skipped. Instead, we signal the end of the string.
	if delta > 0 && state != stateMultilineString {
		pos.Offset--

		return &token{
			Type: tokenType(indentToken),
			Pos:  *pos,
		}, false, false, nil
	}
	if delta < 0 {
		pos.Offset--

		if state == stateMultilineString {
			return nil, false, true, nil
		}

		return &token{
			Type: tokenType(unindentToken),
			Pos:  *pos,
		}, false, false, nil
	}

	return nil, false, false, nil
}

// OpenMultilineString updates the scanner state such that it will honor the indentation level of a multiline string
// starting on the following line. Effectively this means expecting an extra indentation.
func (i *indentationScanner) OpenMultilineString() {
	i.multilineRef = i.currentLine + 2
}
