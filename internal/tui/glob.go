package tui

// globMatch is a tiny redis-style glob matcher: '*' matches any
// (possibly empty) run, '?' matches exactly one rune, everything else
// is literal. Bracket classes are out of scope for the M7 filter.
func globMatch(pattern, s string) bool {
	p := []rune(pattern)
	t := []rune(s)
	return matchRunes(p, t)
}

func matchRunes(p, s []rune) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			// Collapse runs of '*'.
			for len(p) > 0 && p[0] == '*' {
				p = p[1:]
			}
			if len(p) == 0 {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if matchRunes(p, s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 {
				return false
			}
			p = p[1:]
			s = s[1:]
		default:
			if len(s) == 0 || p[0] != s[0] {
				return false
			}
			p = p[1:]
			s = s[1:]
		}
	}
	return len(s) == 0
}
