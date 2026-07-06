package keyguard

import "testing"

func TestValidate(t *testing.T) {
	ok := []string{"", "config", "data/ab/cd", "keys/0123abcd", "index/xyz"}
	for _, k := range ok {
		if err := Validate(k); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", k, err)
		}
	}
	bad := []string{
		"../etc/passwd",
		"data/../../secret",
		"/absolute/key",
		"a/../../b",
		"a/./b",
		"..",
		"data\\..\\x",
		"a\x00b",
	}
	for _, k := range bad {
		if err := Validate(k); err == nil {
			t.Errorf("Validate(%q) = nil, want error", k)
		}
	}
}
