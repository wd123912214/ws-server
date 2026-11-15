package whitelist

import (
	"errors"
	"slices"
	"testing"
)

func TestManagerNormalizesAndChecksUIDs(t *testing.T) {
	m, err := New([]string{"b", "a", "b"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.List(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("list = %v", got)
	}
	if !m.Allowed("a") || m.Allowed("missing") {
		t.Fatal("Allowed returned wrong result")
	}
	if _, err := New([]string{"bad-uid"}, nil); err == nil {
		t.Fatal("invalid UID accepted")
	}
}

func TestReplacePersistsBeforePublishing(t *testing.T) {
	persisted := []string(nil)
	m, err := New([]string{"old"}, func(uids []string) error {
		persisted = append([]string(nil), uids...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Replace([]string{"new2", "new1", "new1"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"new1", "new2"}) || !slices.Equal(persisted, got) || !m.Allowed("new1") || m.Allowed("old") {
		t.Fatalf("replace result=%v persisted=%v", got, persisted)
	}
}

func TestReplaceFailureKeepsOldRuntimeState(t *testing.T) {
	wantErr := errors.New("disk failed")
	m, err := New([]string{"old"}, func([]string) error { return wantErr })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Replace([]string{"new"}); !errors.Is(err, wantErr) {
		t.Fatalf("Replace error = %v", err)
	}
	if !m.Allowed("old") || m.Allowed("new") {
		t.Fatal("runtime state changed after persistence failure")
	}
}

func TestReplaceAllowsEmptyWhitelist(t *testing.T) {
	m, _ := New([]string{"old"}, nil)
	got, err := m.Replace(nil)
	if err != nil || len(got) != 0 || m.Allowed("old") {
		t.Fatalf("empty replace: got=%v err=%v", got, err)
	}
}
