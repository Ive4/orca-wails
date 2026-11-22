//go:build unix

package core

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestSpawnPassesDesktopQuestionKeyOnlyThroughInheritedPipe(t *testing.T) {
	const marker = "ORCA_WAILS_QUESTION_KEY_FD"
	const fixture = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv(marker, "4")

	for name, tc := range map[string]struct {
		key, script, output string
	}{
		"desktop key": {
			key:    fixture,
			script: `test "$ORCA_WAILS_QUESTION_KEY_FD" = 3 && env && printf '\n--fd--\n' && cat <&3`,
			output: fixture,
		},
		"ordinary core": {
			script: `test -z "${ORCA_WAILS_QUESTION_KEY_FD+x}" && env && printf '\n--fd--\n'`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			stdin, err := os.Open(os.DevNull)
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()

			overrides := map[string]string{marker: "5"}
			spec := Spec{Program: "/bin/sh", Args: []string{"-c", tc.script}, Env: overrides, DesktopQuestionKey: tc.key}
			child, err := spawn(spec, stdin, writer, writer)
			writer.Close()
			if err != nil {
				t.Fatal(err)
			}
			contents, readErr := io.ReadAll(reader)
			state, waitErr := child.leader.Wait()
			if readErr != nil || waitErr != nil || !state.Success() {
				t.Fatalf("could not read child environment and fd: read=%v wait=%v", readErr, waitErr)
			}
			env, key, ok := strings.Cut(string(contents), "\n--fd--\n")
			if !ok || key != tc.output || strings.Contains(env, fixture) ||
				(tc.key == "" && strings.Contains(env, marker)) {
				t.Fatal("child did not receive the expected isolated key pipe and environment")
			}
			if overrides[marker] != "5" {
				t.Fatal("spawn mutated the caller environment")
			}
		})
	}
}

func TestSpawnRejectsInvalidDesktopQuestionKey(t *testing.T) {
	for _, key := range []string{"secret", strings.Repeat("A", 64), strings.Repeat("g", 64), strings.Repeat("0", 65)} {
		spec := Spec{Program: "/usr/bin/true", DesktopQuestionKey: key}
		child, err := spawn(spec, nil, nil, nil)
		if child != nil {
			_, _ = child.leader.Wait()
		}
		if child != nil || err == nil || strings.Contains(err.Error(), key) {
			t.Fatal("invalid key reached the child or leaked into the error")
		}
	}
}
