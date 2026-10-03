package main

import "testing"

func TestConsoleTrust(t *testing.T) {
	if consoleTrust(false) != nil {
		t.Error("a non-elevated console should trust any config")
	}
	if consoleTrust(true) == nil {
		t.Error("an elevated console must use the admin-only trust check")
	}
}
