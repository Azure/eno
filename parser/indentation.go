package parser

type indentationScanner struct {
	// previousLineIndentSpaces is used as lookbehind to the previous line's level of indentation.
	// Necessary to determine if the current line has changed the level of indentation.
	previousLineIndentSpaces int

	// currentLineIndentSpaces maintains a space counter while consuming the indentation of the current line.
	// Can be compared to previousLineIndentSpaces when hasPassedIndentation == true.
	currentLineIndentSpaces int

	// multilineReferenceSpaces is kind of like previousLineIndentSpaces, but for multi-line strings.
	// Set to the value of currentLineIndentSpaces + 2 when a multi-line string is opened.
	// When currentLineIndentSpaces > multilineReferenceSpaces, the multi-line string has been terminated.
	multilineReferenceSpaces int

	// hasPassedIndentation is true after the first non-whitespace character has been seen on the current line.
	hasPassedIndentation bool
}

func (i *indentationScanner) Scan(state lexerState, inExpr bool, pos *position, b byte) (*token, bool /* skip */, bool /* break */, error) {
	if inExpr && b == '\n' {
		// Ignore newlines in expressions so they can span multiple lines if needed
		return nil, false, false, nil
	}

	if i.hasPassedIndentation {
		if b != '\n' {
			// Ignore characters that are not applicable
			return nil, false, false, nil
		}

		// Reset the per-line state at each newline character
		i.currentLineIndentSpaces = 0
		i.hasPassedIndentation = false
		return nil, false, false, nil
	}

	// In multi-line strings, don't consume more indentation than the reference amount e.g. indentation of first line.
	// Otherwise we will chomp any leading spaces within the string.
	if state != stateMultilineString || i.currentLineIndentSpaces < i.multilineReferenceSpaces {
		if b == ' ' {
			i.currentLineIndentSpaces++
			return nil, true, false, nil
		}
		if b == '\t' {
			i.currentLineIndentSpaces += 2
			return nil, true, false, nil
		}
	}

	// Indentation must be increments of two spaces
	if i.currentLineIndentSpaces%2 != 0 {
		return nil, false, false, ErrOddIndentation
	}

	// If we made it this far into the func then this line's indentation is complete
	i.hasPassedIndentation = true

	delta := i.currentLineIndentSpaces - i.previousLineIndentSpaces
	i.previousLineIndentSpaces = i.currentLineIndentSpaces

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
	i.multilineReferenceSpaces = i.currentLineIndentSpaces + 2
}
