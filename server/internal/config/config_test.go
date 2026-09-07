package config

import (
	"os"
	"testing"
)

func TestEnvIntParsesValid(t *testing.T) {
	os.Setenv("TEST_ENV_INT_VALID", "123")
	defer os.Unsetenv("TEST_ENV_INT_VALID")
	got := envInt("TEST_ENV_INT_VALID", 0)
	if got != 123 {
		t.Errorf("envInt valid = %d, want 123", got)
	}
}

func TestEnvIntFallsBackOnEmpty(t *testing.T) {
	os.Setenv("TEST_ENV_INT_EMPTY", "")
	defer os.Unsetenv("TEST_ENV_INT_EMPTY")
	got := envInt("TEST_ENV_INT_EMPTY", 456)
	if got != 456 {
		t.Errorf("envInt empty = %d, want 456", got)
	}
}

func TestEnvIntFallsBackOnGarbage(t *testing.T) {
	os.Setenv("TEST_ENV_INT_GARBAGE", "not_a_number")
	defer os.Unsetenv("TEST_ENV_INT_GARBAGE")
	got := envInt("TEST_ENV_INT_GARBAGE", 789)
	if got != 789 {
		t.Errorf("envInt garbage = %d, want 789", got)
	}
}

func TestEnvIntFallsBackOnUnset(t *testing.T) {
	os.Unsetenv("TEST_ENV_INT_UNSET")
	got := envInt("TEST_ENV_INT_UNSET", 999)
	if got != 999 {
		t.Errorf("envInt unset = %d, want 999", got)
	}
}
