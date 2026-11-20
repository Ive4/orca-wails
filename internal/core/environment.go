package core

import (
	"maps"
	"os"
	"slices"
	"strings"
)

func runtimeEnvironment(overrides map[string]string, foldCase bool) []string {
	normalize := func(key string) string {
		if foldCase {
			return strings.ToUpper(key)
		}
		return key
	}

	env := make(map[string]string)
	for _, entry := range os.Environ() {
		// Windows also supplies drive working directories as entries like =C:=C:\dir.
		if entry == "" {
			continue
		}
		end := strings.IndexByte(entry[1:], '=')
		if end < 0 {
			continue
		}
		key := normalize(entry[:end+1])
		switch key {
		case "ELECTRON_RUN_AS_NODE", "ORCA_APP_EXECUTABLE", "NODE_OPTIONS":
			continue
		}
		env[key] = entry
	}
	for _, key := range slices.Sorted(maps.Keys(overrides)) {
		env[normalize(key)] = key + "=" + overrides[key]
	}
	result := make([]string, 0, len(env))
	for _, key := range slices.Sorted(maps.Keys(env)) {
		result = append(result, env[key])
	}
	return result
}
