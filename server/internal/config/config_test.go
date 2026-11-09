package config

import "testing"

func TestBootstrapPasswordCharacterMinimum(t *testing.T) {
	for _, test := range []struct {
		name     string
		password string
		valid    bool
	}{
		{name: "three_chinese_characters", password: "一二三", valid: false},
		{name: "eight_chinese_characters", password: "一二三四五六七八", valid: true},
		{name: "whitespace_counts_and_is_preserved", password: " 一二三四五六 ", valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("LINKNESTX_BOOTSTRAP_ADMIN_USERNAME", "admin")
			t.Setenv("LINKNESTX_BOOTSTRAP_ADMIN_PASSWORD", test.password)
			cfg, err := Load()
			if !test.valid {
				if err == nil {
					t.Fatal("three-character bootstrap password accepted")
				}
				return
			}
			if err != nil {
				t.Fatalf("eight-character bootstrap password rejected: %v", err)
			}
			if cfg.BootstrapAdminPassword != test.password {
				t.Fatal("bootstrap password was changed")
			}
		})
	}
}
