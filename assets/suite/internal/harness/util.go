package harness

import "strings"

// Helpers shared by the store adapters. They live in the core so that a
// suite selecting more than one store does not get two definitions.

func firstLine(query string) string {
	q := strings.TrimSpace(strings.ReplaceAll(query, "\n", " "))
	for strings.Contains(q, "  ") {
		q = strings.ReplaceAll(q, "  ", " ")
	}
	if len(q) > 90 {
		return q[:90] + "…"
	}
	return q
}

func sanitize(name string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			return r
		}
		return '_'
	}, name)
}

// normalizeCell turns driver []byte into string, so assertions compare against
// what the column means rather than against a byte slice.
func normalizeCell(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

func redactDSN(dsn string) string {
	if i := strings.Index(dsn, "@"); i > 0 {
		if j := strings.Index(dsn, "://"); j > 0 && j+3 < i {
			return dsn[:j+3] + "***" + dsn[i:]
		}
	}
	return dsn
}
