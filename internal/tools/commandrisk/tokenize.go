package commandrisk

import "strings"

// Token is the small amount of shell syntax the command-risk classifier needs.
// It is deliberately not a full shell AST: the gate must fail closed when the
// input cannot be represented by this tokenizer.
type Token struct {
	Text                       string
	ReceivesPipe               bool
	IsRedirectTarget           bool
	IsTruncatingRedirectTarget bool
	IsOperator                 bool
	Malformed                  bool
}

// Tokenize splits a shell command while preserving quoted words and the
// operators that separate command segments. Unsupported or incomplete shell
// syntax is marked as Malformed so callers can choose a conservative verdict.
func Tokenize(command string) []Token {
	var tokens []Token
	var word strings.Builder
	var quote rune
	var escaped bool
	var malformed bool
	redirectTarget := false
	anyRedirectTarget := false
	substitutionDepth := 0
	groupDepth := 0

	flush := func() {
		if word.Len() == 0 {
			return
		}
		tokens = append(tokens, Token{
			Text:                       word.String(),
			IsRedirectTarget:           anyRedirectTarget,
			IsTruncatingRedirectTarget: redirectTarget,
			Malformed:                  malformed,
		})
		word.Reset()
		redirectTarget = false
		anyRedirectTarget = false
	}
	missingRedirect := func() {
		if !anyRedirectTarget {
			return
		}
		tokens = append(tokens, Token{Text: "<missing redirect target>", IsOperator: true, Malformed: true})
		redirectTarget = false
		anyRedirectTarget = false
	}
	operator := func(text string) {
		flush()
		missingRedirect()
		tokens = append(tokens, Token{Text: text, IsOperator: true})
	}

	for i := 0; i < len(command); i++ {
		c := rune(command[i])

		if escaped {
			word.WriteByte(command[i])
			escaped = false
			continue
		}

		if quote == '\'' {
			if c == '\'' {
				quote = 0
			} else {
				word.WriteByte(command[i])
			}
			continue
		}
		if quote == '"' {
			switch c {
			case '"':
				quote = 0
			case '\\':
				if i+1 >= len(command) {
					malformed = true
				} else {
					escaped = true
				}
			default:
				word.WriteByte(command[i])
			}
			continue
		}

		// Keep command substitutions together. Their contents are recursively
		// assessed later, but treating their operators as outer separators here
		// would make it too easy to miss a destructive nested command.
		if substitutionDepth > 0 {
			switch c {
			case '\'':
				quote = c
				word.WriteByte(command[i])
			case '"':
				quote = c
				word.WriteByte(command[i])
			case '\\':
				escaped = true
				word.WriteByte(command[i])
			case '(':
				substitutionDepth++
				word.WriteByte(command[i])
			case ')':
				substitutionDepth--
				word.WriteByte(command[i])
			default:
				word.WriteByte(command[i])
			}
			continue
		}

		switch c {
		case '\\':
			if i+1 >= len(command) {
				malformed = true
			} else {
				escaped = true
			}
		case '\'', '"':
			quote = c
		case '$':
			word.WriteByte(command[i])
			if i+1 < len(command) && command[i+1] == '(' {
				word.WriteByte(command[i+1])
				i++
				substitutionDepth = 1
			} else if i+1 < len(command) && (command[i+1] == '\'' || command[i+1] == '"') {
				// Bash ANSI-C/locale quoting is not represented by this
				// tokenizer. Keep the command fail-closed instead of turning
				// $'/etc' into a harmless-looking workspace-relative word.
				malformed = true
			}
		case ' ', '\t', '\r':
			flush()
		case '\n', ';':
			operator(string(c))
		case '&':
			if i+1 < len(command) && command[i+1] == '&' {
				operator("&&")
				i++
			} else {
				operator("&")
			}
		case '|':
			if i+1 < len(command) && command[i+1] == '|' {
				operator("||")
				i++
			} else if i+1 < len(command) && command[i+1] == '&' {
				operator("|&")
				i++
			} else {
				operator("|")
			}
		case '(':
			operator("(")
			groupDepth++
		case ')':
			if groupDepth == 0 {
				malformed = true
			} else {
				groupDepth--
			}
			operator(")")
		case '>':
			flush()
			missingRedirect()
			anyRedirectTarget = true
			if i+1 < len(command) && command[i+1] == '>' {
				i++ // append-only redirect; it cannot truncate an existing file
				redirectTarget = false
			} else if i+1 < len(command) && command[i+1] == '&' {
				// File-descriptor duplication (`>&2`, `2>&1`) is a redirect for
				// policy purposes but does not truncate a filesystem target.
				i++
				redirectTarget = false
			} else {
				redirectTarget = true
			}
		case '<':
			flush()
			missingRedirect()
			// Here-doc/process-substitution syntax is intentionally opaque.
			malformed = true
		default:
			word.WriteByte(command[i])
		}
	}

	if quote != 0 || escaped || substitutionDepth != 0 || groupDepth != 0 {
		malformed = true
	}
	flush()
	missingRedirect()
	if validateOperators(tokens) {
		malformed = true
	}
	if len(tokens) > 0 && tokens[len(tokens)-1].IsOperator {
		switch tokens[len(tokens)-1].Text {
		case "&&", "||", "|", "|&", "(":
			tokens[len(tokens)-1].Malformed = true
		}
	}
	if malformed && len(tokens) == 0 {
		tokens = append(tokens, Token{Text: "<malformed command>", Malformed: true})
	} else if malformed {
		// If malformed syntax occurred after a word was flushed, retain a
		// separate marker so the assessment cannot accidentally treat the
		// remaining command as safe.
		seen := false
		for _, tok := range tokens {
			if tok.Malformed {
				seen = true
				break
			}
		}
		if !seen {
			tokens = append(tokens, Token{Text: "<malformed command>", Malformed: true})
		}
	}
	return tokens
}

func validateOperators(tokens []Token) bool {
	bad := false
	previousOperator := ""
	for i := range tokens {
		tok := &tokens[i]
		if !tok.IsOperator {
			previousOperator = ""
			continue
		}
		switch tok.Text {
		case ";", "&&", "||", "|", "|&", "&":
			if i == 0 || previousOperator == ";" || previousOperator == "&&" || previousOperator == "||" || previousOperator == "|" || previousOperator == "|&" || previousOperator == "&" || previousOperator == "(" {
				tok.Malformed = true
				bad = true
			}
		case ")":
			if previousOperator == "(" {
				tok.Malformed = true
				bad = true
			}
		}
		previousOperator = tok.Text
	}
	return bad
}

// SplitSegments returns command words grouped around shell command
// separators. Operators themselves are omitted. The first word after a pipe
// (and its arguments) is marked as receiving piped input.
func SplitSegments(tokens []Token) [][]Token {
	var segments [][]Token
	var current []Token
	receivesPipe := false
	flush := func() {
		if len(current) > 0 {
			segments = append(segments, current)
			current = nil
		}
	}
	for _, tok := range tokens {
		if tok.IsOperator {
			flush()
			switch tok.Text {
			case "|", "|&":
				receivesPipe = true
			default:
				receivesPipe = false
			}
			if tok.Malformed {
				segments = append(segments, []Token{tok})
			}
			continue
		}
		tok.ReceivesPipe = receivesPipe
		current = append(current, tok)
	}
	flush()
	return segments
}
