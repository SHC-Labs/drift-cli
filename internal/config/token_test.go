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
		{"v1 explicit hex", "drift_v1_abcdef0123456789", "v1", false},
		{"v1 long hex", "drift_v1_a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0", "v1", false},
		{"v1 base64url", "drift_v1_wfPENXRhMVtOojQcsqw76f1CP1D-h5zTSU4eqEblRLKscj5Q2bB833O2vTFldc9T", "v1", false},
		{"legacy hex 16", "drift_abcdef0123456789", "legacy", false},
		{"legacy hex long", "drift_a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8", "legacy", false},
		{"dashboard base64url 64", "drift_wfPENXRhMVtOojQcsqw76f1CP1D-h5zTSU4eqEblRLKscj5Q2bB833O2vTFldc9T", "legacy", false},
		{"base64url with underscore", "drift_AAAA_BBBB_CCCC_DDDD", "legacy", false},
		{"base64url with dash", "drift_AAAA-BBBB-CCCC-DDDD", "legacy", false},

		{"missing prefix", "noprefix_abc", "", true},
		{"empty after prefix", "drift_", "", true},
		{"empty v1 payload", "drift_v1_", "", true},
		{"unknown version v2", "drift_v2_abcdefghij", "", true},
		{"unknown version v9", "drift_v9_abcdefghij", "", true},
		{"v1 with plus (not base64url)", "drift_v1_AAAA+BBBB+CCCC", "", true},
		{"v1 with slash (not base64url)", "drift_v1_AAAA/BBBB/CCCC", "", true},
		{"v1 with space", "drift_v1_AAAA BBBB", "", true},
		{"legacy too short", "drift_abc", "", true},
		{"legacy with plus", "drift_AAAA+BBBB+CCCC+DDDD", "", true},
		{"legacy with slash", "drift_AAAA/BBBB/CCCC/DDDD", "", true},
		{"legacy with space", "drift_AAAA BBBB CCCC DDDD", "", true},
		{"sneaky v2x", "drift_v2x_abcdefghij", "", true},
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
