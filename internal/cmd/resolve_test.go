package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sweetrpg/sweetrpg-cli/internal/client"
)

func stubOps(name string, candidates []candidate, calls *int) entityOps {
	return entityOps{
		spec: client.EntityType{Name: name, Plural: name + "s"},
		find: func(context.Context, *client.Client, string) ([]candidate, error) {
			*calls++
			return candidates, nil
		},
	}
}

func resetResolveState(t *testing.T) {
	t.Helper()
	oldTTY, oldYes := stdoutIsTTY, flagYes
	t.Cleanup(func() { stdoutIsTTY = oldTTY; flagYes = oldYes })
	stdoutIsTTY = func() bool { return false }
	flagYes = false
}

func TestResolveRefIDShortCircuits(t *testing.T) {
	calls := 0
	ops := entityOps{
		spec: client.EntityType{Name: "volume", Plural: "volumes"},
		find: func(context.Context, *client.Client, string) ([]candidate, error) {
			calls++
			return nil, nil
		},
	}
	id, err := resolveRef(context.Background(), nil, ops, "507f1f77bcf86cd799439011")
	if err != nil || id != "507f1f77bcf86cd799439011" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if calls != 0 {
		t.Errorf("find called %d times for ID-shaped ref", calls)
	}
}

func TestResolveRefUniqueMatchResolves(t *testing.T) {
	resetResolveState(t)
	calls := 0
	ops := stubOps("publisher", []candidate{{ID: "pub1", Label: "Evil Hat"}}, &calls)
	id, err := resolveRef(context.Background(), nil, ops, "Evil Hat")
	if err != nil || id != "pub1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
}

func TestResolveRefNoMatchErrors(t *testing.T) {
	resetResolveState(t)
	calls := 0
	ops := stubOps("system", nil, &calls)
	_, err := resolveRef(context.Background(), nil, ops, "Nonexistent RPG")
	if err == nil || !strings.Contains(err.Error(), `no system record found for "Nonexistent RPG"`) {
		t.Fatalf("want no-match error, got %v", err)
	}
}

func TestResolveRefAmbiguousNonTTYFailsClosed(t *testing.T) {
	resetResolveState(t)
	calls := 0
	ops := stubOps("volume", []candidate{
		{ID: "v1", Label: "Core Rules"},
		{ID: "v2", Label: "Core Rules", Detail: "5e"},
	}, &calls)
	pickCandidate = func([]candidate) (int, error) {
		t.Fatal("picker must not run in non-TTY mode")
		return 0, nil
	}
	_, err := resolveRef(context.Background(), nil, ops, "Core Rules")
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	for _, want := range []string{"v1", "v2", "ambiguous"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}

func TestResolveRefYesFlagFailsClosedEvenOnTTY(t *testing.T) {
	resetResolveState(t)
	stdoutIsTTY = func() bool { return true }
	flagYes = true
	calls := 0
	ops := stubOps("volume", []candidate{{ID: "v1"}, {ID: "v2"}}, &calls)
	_, err := resolveRef(context.Background(), nil, ops, "Core Rules")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("want ambiguity error with --yes, got %v", err)
	}
}

func TestResolveRefPickerChoiceWins(t *testing.T) {
	resetResolveState(t)
	stdoutIsTTY = func() bool { return true }
	calls := 0
	ops := stubOps("volume", []candidate{{ID: "v1"}, {ID: "v2"}}, &calls)
	pickCandidate = func(cands []candidate) (int, error) { return 1, nil }
	id, err := resolveRef(context.Background(), nil, ops, "Core Rules")
	if err != nil || id != "v2" {
		t.Fatalf("id=%q err=%v", id, err)
	}
}

func TestResolveRefPickerDeclineErrors(t *testing.T) {
	resetResolveState(t)
	stdoutIsTTY = func() bool { return true }
	calls := 0
	ops := stubOps("volume", []candidate{{ID: "v1"}, {ID: "v2"}}, &calls)
	pickCandidate = func([]candidate) (int, error) { return 0, errors.New("aborted") }
	_, err := resolveRef(context.Background(), nil, ops, "Core Rules")
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("want cancellation error, got %v", err)
	}
}

func TestConfirmDeleteForceSkipsPrompt(t *testing.T) {
	resetResolveState(t)
	cmd := newDeleteCommand()
	// --force is a child flag; setting it here does not reach the package var,
	// so set the var directly.
	flagForce = true
	defer func() { flagForce = false }()
	err := confirmDelete(cmd, "studio", "abc")
	if err != nil {
		t.Fatalf("force should skip confirmation, got %v", err)
	}
}

func TestConfirmDeleteNonTTYRefusedWithoutForce(t *testing.T) {
	resetResolveState(t)
	flagForce = false
	cmd := newDeleteCommand()
	err := confirmDelete(cmd, "studio", "abc")
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("want refusal naming --force, got %v", err)
	}
}

func TestConfirmDeleteInteractiveAcceptedAndDeclined(t *testing.T) {
	resetResolveState(t)
	stdoutIsTTY = func() bool { return true }
	cmd := newDeleteCommand()

	confirmPrompt = func(string) bool { return true }
	if err := confirmDelete(cmd, "studio", "abc"); err != nil {
		t.Fatalf("confirmed delete should proceed, got %v", err)
	}

	confirmPrompt = func(string) bool { return false }
	if err := confirmDelete(cmd, "studio", "abc"); !errors.Is(err, ErrDeclined) {
		t.Fatalf("want ErrDeclined, got %v", err)
	}
}
