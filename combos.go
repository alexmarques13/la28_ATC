package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

type Combo struct {
	Email    string
	Password string
}

func loadCombos(path string) ([]Combo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Combo
	sc := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)

	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		email, pass, ok := parseComboLine(line)
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected email:pass, got %q", path, lineNo, line)
		}
		out = append(out, Combo{Email: email, Password: pass})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no combos found (expected email:pass lines)", path)
	}
	return out, nil
}

func parseComboLine(line string) (email, pass string, ok bool) {
	i := strings.IndexByte(line, ':')
	if i <= 0 || i >= len(line)-1 {
		return "", "", false
	}
	email = strings.TrimSpace(line[:i])
	pass = strings.TrimSpace(line[i+1:])
	if email == "" || pass == "" {
		return "", "", false
	}
	return email, pass, true
}
