// Package envfile lets settings be read from files named by NAME_FILE.
package envfile

import (
	"fmt"
	"os"
	"strings"
)

// WithFiles returns a getenv that also reads settings from files: when
// NAME_FILE is set, NAME is the contents of the file it names, minus one
// trailing newline. Production keeps its secrets in files mounted into the
// container, so they aren't in the container's environment (docker
// inspect, /proc). Every file is read now; one that can't be read, or a
// setting given both ways, is an error.
func WithFiles(environ []string, getenv func(string) string) (func(string) string, error) {
	files := map[string]string{}
	for _, kv := range environ {
		k, path, _ := strings.Cut(kv, "=")
		name, ok := strings.CutSuffix(k, "_FILE")
		if !ok || name == "" || path == "" {
			continue
		}
		if getenv(name) != "" {
			return nil, fmt.Errorf("both %s and %s are set", name, k)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		files[name] = strings.TrimSuffix(string(b), "\n")
	}
	return func(name string) string {
		if v, ok := files[name]; ok {
			return v
		}
		return getenv(name)
	}, nil
}
