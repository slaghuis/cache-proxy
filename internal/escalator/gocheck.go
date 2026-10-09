package escalator

import (
	"go/parser"
	"go/token"
	"regexp"
	"strings"
)

var goCodeBlockRe = regexp.MustCompile("(?s)```go\\s*\\n(.*?)\\n```")

// CompileSignal returns 1.0 if all extracted Go blocks parse,
// 0.0 if any fails, and 1.0 (unused) if no Go blocks are present.
func CompileSignal(response string) Signal {
	s := Signal{Name: "go_parse", Weight: 2.5, Score: 1.0}

	matches := goCodeBlockRe.FindAllStringSubmatch(response, -1)
	if len(matches) == 0 {
		// No Go code blocks. Signal is neutral (doesn't apply).
		s.Weight = 0 // zero weight = ignored by combiner
		return s
	}

	for i, m := range matches {
		code := m[1]
		if !strings.Contains(code, "package ") &&
			!strings.Contains(code, "func ") &&
			!strings.Contains(code, "type ") {
			continue // snippet-level, skip
		}

		wrapped := ensureParseable(code)
		fset := token.NewFileSet()
		_, err := parser.ParseFile(fset, "snippet.go", wrapped, parser.AllErrors)
		if err != nil {
			s.Score = 0.1
			s.Reason = "Go block " + itoa(i+1) + " did not parse: " +
				truncateErr(err.Error())
			return s
		}
	}
	return s
}

// ensureParseable wraps bare function/method blocks in a package decl so
// go/parser accepts snippet-style code.
func ensureParseable(code string) string {
	if strings.Contains(code, "package ") {
		return code
	}
	// Pure function body?
	if strings.HasPrefix(strings.TrimSpace(code), "func ") ||
		strings.HasPrefix(strings.TrimSpace(code), "type ") ||
		strings.HasPrefix(strings.TrimSpace(code), "var ") ||
		strings.HasPrefix(strings.TrimSpace(code), "const ") {
		return "package snippet\n\n" + code
	}
	// Statement block — wrap in a func.
	return "package snippet\n\nfunc _snippet() {\n" + code + "\n}\n"
}

func truncateErr(s string) string {
	if len(s) <= 120 {
		return s
	}
	return s[:120] + "…"
}

func itoa(i int) string {
	return strings.TrimLeft(strings.Repeat("0", 2-len(itoaRaw(i)))+itoaRaw(i), "0")
}

func itoaRaw(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	s := string(b[n:])
	if neg {
		s = "-" + s
	}
	return s
}