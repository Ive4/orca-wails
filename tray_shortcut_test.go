package main

import (
	"errors"
	"slices"
	"testing"
)

type testShortcutRegistrar struct {
	active  []string
	refused string
}

func (r *testShortcutRegistrar) Register(key string, _ func()) error {
	if key == r.refused {
		return errors.New("in use")
	}
	r.active = append(r.active, key)
	return nil
}

func (r *testShortcutRegistrar) IsRegistered(key string) bool { return slices.Contains(r.active, key) }

func (r *testShortcutRegistrar) Unregister(key string) error {
	r.active = slices.DeleteFunc(r.active, func(active string) bool { return active == key })
	return nil
}

func TestTrayShortcutPreservesBindingWhenSettingCannotBeSaved(t *testing.T) {
	for _, refused := range []string{"", "cmd+shift+k"} {
		registrar := &testShortcutRegistrar{active: []string{"cmd+shift+o"}, refused: refused}
		err := replaceTrayShortcut(registrar, "Cmd+Shift+O", "Cmd+Shift+K", func() {}, func() error {
			return errors.New("cannot save")
		})
		if err == nil || !slices.Equal(registrar.active, []string{"cmd+shift+o"}) {
			t.Fatalf("active bindings = %v, err = %v", registrar.active, err)
		}
	}
}
