package config

import (
	"errors"
	"testing"
)

func TestValidateTokenV1(t *testing.T) {
	cases := []struct {
		name    string
		token   string
		wantVer string
		wantErr bool
	}{
		{"v1 explicit", "drift_v1_abcdef0123456789", "v1", false},
		{"v1 long payload", "drift_v1_a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0", "v1", false},
		{"legacy hex 16", "drift_abcdef0123456789", "legacy", false},
		{"legacy hex long", "drift_a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8", "legacy", false},

		{"missing prefix", "noprefix_abc", "", true},
		{"empty after prefix", "drift_", "", true},
		{"empty v1 payload", "drift_v1_", "", true},
		{"unknown version v2", "drift_v2_abc", "", true},
		{"unknown version v9", "drift_v9_abc", "", true},
		{"v1 with non-hex payload", "drift_v1_GGGG", "", true},
		{"legacy too short", "drift_abc", "", true},
		{"legacy non-hex", "drift_GGGGGGGGGGGGGGGG", "", true},
		{"sneaky v2x", "drift_v2x_abc", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotVer, err := ValidateToken(tc.token)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got version=%q", gotVer)
				}
				if err != nil && !errors.Is(err, ErrTokenFormat) {
					t.Errorf("error not wrapping ErrTokenFormat: %v", err)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if gotVer != tc.wantVer {
				t.Errorf("version = %q, want %q", gotVer, tc.wantVer)
			}
		})
	}
}
