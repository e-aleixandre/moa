package core

import "testing"

func TestSameModelIdentityNormalizesAliases(t *testing.T) {
	if !SameModelIdentity("fable", "claude-fable-5") {
		t.Fatal("alias and effective ID should match")
	}
	if SameModelIdentity("fable", "claude-opus-5") {
		t.Fatal("different effective model should not match")
	}
}
