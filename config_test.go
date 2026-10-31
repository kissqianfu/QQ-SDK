package qqbot

import (
	"strings"
	"testing"
)

func TestConfigValidate(t *testing.T) {
	valid := Config{AppID: "app", AppSecret: "secret"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate(valid) error = %v", err)
	}

	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "no appid", cfg: Config{AppSecret: "s"}, want: "AppID"},
		{name: "no secret", cfg: Config{AppID: "a"}, want: "AppSecret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() = %v, want error mentioning %q", err, tt.want)
			}
		})
	}
}
