package migrate

// configContext tracks lexical boundaries without parsing or reserializing unrelated TOML values.
type configContext struct {
	// quote identifies the open basic or literal string delimiter, or zero outside strings.
	quote byte
	// multiline keeps triple-quoted strings open across physical lines.
	multiline bool
	// depth counts open array and inline-table delimiters outside strings and comments.
	depth int
}

// scan reports whether a line starts a statement and advances context across its original bytes.
func (c *configContext) scan(line string) bool {
	statement := c.quote == 0 && c.depth == 0
	for index := 0; index < len(line); index++ {
		char := line[index]
		if c.quote != 0 {
			if c.quote == '"' && char == '\\' {
				index++
				continue
			}
			if char != c.quote {
				continue
			}
			if !c.multiline {
				c.quote = 0
				continue
			}
			if index+2 < len(line) && line[index+1] == char && line[index+2] == char {
				// TOML permits one or two content quotes immediately before the closing triple.
				for index+1 < len(line) && line[index+1] == char {
					index++
				}
				c.quote, c.multiline = 0, false
			}
			continue
		}
		switch char {
		case '#':
			return statement
		case '\'', '"':
			c.quote = char
			if index+2 < len(line) && line[index+1] == char && line[index+2] == char {
				c.multiline = true
				index += 2
			}
		case '[', '{':
			c.depth++
		case ']', '}':
			if c.depth > 0 {
				c.depth--
			}
		}
	}
	return statement
}
