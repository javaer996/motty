package cmdhistory

import (
	"reflect"
	"testing"
	"time"
)

func TestSuggestCommandsCompletesOnlyTheCommandWord(t *testing.T) {
	now := time.Now()
	entries := []Entry{
		{Command: "lsof -i :8080", Used: now, Count: 2},
		{Command: "lsof -p 42", Used: now.Add(-time.Hour), Count: 1},
		{Command: "ls -lah", Used: now.Add(-2 * time.Hour), Count: 5},
	}

	// Two remembered lsof lines must collapse into one command-word suffix.
	got := SuggestCommands(entries, "lso", 5)
	if !reflect.DeepEqual(got, []string{"f"}) {
		t.Fatalf("SuggestCommands(lso) = %v", got)
	}

	// An exact command word has nothing left to complete, and a line that
	// already has a space is not a command word.
	if got := SuggestCommands(entries, "lsof", 5); len(got) != 0 {
		t.Fatalf("SuggestCommands(lsof) = %v", got)
	}
	if got := SuggestCommands(entries, "lsof ", 5); len(got) != 0 {
		t.Fatalf("SuggestCommands(lsof ) = %v", got)
	}
}

func TestSuggestCommandsRanksByUse(t *testing.T) {
	now := time.Now()
	entries := []Entry{
		{Command: "ls -lah", Used: now, Count: 1},
		{Command: "lsof -i", Used: now.Add(-time.Hour), Count: 9},
	}

	got := SuggestCommands(entries, "l", 5)
	if !reflect.DeepEqual(got, []string{"sof", "s"}) {
		t.Fatalf("SuggestCommands(l) = %v", got)
	}
}
