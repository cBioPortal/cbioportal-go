package databin

// isCreatable mirrors org.apache.commons.lang3.math.NumberUtils.isCreatable
// (commons-lang3 3.17): it accepts decimal, scientific, hexadecimal ("0x1F")
// and octal ("017") literals and Java type suffixes ("5L", "1.5f", "2d").
func isCreatable(str string) bool {
	if str == "" {
		return false
	}
	chars := []rune(str)
	sz := len(chars)
	hasExp := false
	hasDecPoint := false
	allowSigns := false
	foundDigit := false
	start := 0
	if chars[0] == '-' || chars[0] == '+' {
		start = 1
	}
	if sz > start+1 && chars[start] == '0' && !containsRune(chars, '.') {
		if chars[start+1] == 'x' || chars[start+1] == 'X' {
			i := start + 2
			if i == sz {
				return false
			}
			for ; i < sz; i++ {
				if !isHex(chars[i]) {
					return false
				}
			}
			return true
		}
		if isJavaDigit(chars[start+1]) {
			for i := start + 1; i < sz; i++ {
				if chars[i] < '0' || chars[i] > '7' {
					return false
				}
			}
			return true
		}
	}
	sz--
	i := start
	for i < sz || (i < sz+1 && allowSigns && !foundDigit) {
		c := chars[i]
		switch {
		case c >= '0' && c <= '9':
			foundDigit = true
			allowSigns = false
		case c == '.':
			if hasDecPoint || hasExp {
				return false
			}
			hasDecPoint = true
		case c == 'e' || c == 'E':
			if hasExp || !foundDigit {
				return false
			}
			hasExp = true
			allowSigns = true
		case c == '+' || c == '-':
			if !allowSigns {
				return false
			}
			allowSigns = false
			foundDigit = false
		default:
			return false
		}
		i++
	}
	if i < len(chars) {
		c := chars[i]
		if c >= '0' && c <= '9' {
			return true
		}
		if c == 'e' || c == 'E' {
			return false
		}
		if c == '.' {
			if hasDecPoint || hasExp {
				return false
			}
			return foundDigit
		}
		if !allowSigns && (c == 'd' || c == 'D' || c == 'f' || c == 'F') {
			return foundDigit
		}
		if c == 'l' || c == 'L' {
			return foundDigit && !hasExp && !hasDecPoint
		}
		return false
	}
	return !allowSigns && foundDigit
}

func containsRune(chars []rune, r rune) bool {
	for _, c := range chars {
		if c == r {
			return true
		}
	}
	return false
}

func isHex(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// isJavaDigit approximates Character.isDigit for the octal check; only
// ASCII digits can pass the octal loop that follows it.
func isJavaDigit(c rune) bool {
	return c >= '0' && c <= '9'
}
