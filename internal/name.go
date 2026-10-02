package internal

func ValidName(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if c == '_' || ('A' <= c && c <= 'Z') {
			continue
		}
		if i > 0 && '0' <= c && c <= '9' {
			continue
		}
		return false
	}
	return true
}
