package profiles

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

type section struct {
	name   string
	values map[string]string
}

// readINI rejects duplicate keys/sections rather than silently changing which
// profile an ambiguous file selects. Values are not unquoted or interpolated.
func readINI(path string) ([]section, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var sections []section
	seen := map[string]bool{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for line := 1; scanner.Scan(); line++ {
		s := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if s == "" || strings.HasPrefix(s, ";") || strings.HasPrefix(s, "#") {
			continue
		}
		if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
			name := strings.TrimSpace(s[1 : len(s)-1])
			if seen[name] {
				return nil, fmt.Errorf("%s:%d duplicate section %q", path, line, name)
			}
			seen[name] = true
			sections = append(sections, section{name, map[string]string{}})
			continue
		}
		key, value, ok := strings.Cut(s, "=")
		key = strings.TrimSpace(key)
		if !ok || len(sections) == 0 || key == "" {
			return nil, fmt.Errorf("%s:%d invalid INI entry", path, line)
		}
		values := sections[len(sections)-1].values
		if _, ok := values[key]; ok {
			return nil, fmt.Errorf("%s:%d duplicate key %q", path, line, key)
		}
		values[key] = strings.TrimSpace(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return sections, nil
}
