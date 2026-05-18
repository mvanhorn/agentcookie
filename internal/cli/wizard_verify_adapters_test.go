package cli

import (
	"testing"

	"github.com/mvanhorn/agentcookie/internal/state"
)

func TestFormatAdapterRow_Cases(t *testing.T) {
	cases := []struct {
		name       string
		input      state.AdapterResult
		wantStatus string
		wantDetail string
	}{
		{
			name:       "success",
			input:      state.AdapterResult{Name: "instacart-pp-cli", Pushed: 7},
			wantStatus: "ok",
			wantDetail: "",
		},
		{
			name:       "skipped",
			input:      state.AdapterResult{Name: "pagliacci-pp-cli", Skipped: true, SkippedReason: "CLI not installed"},
			wantStatus: "skip",
			wantDetail: "CLI not installed",
		},
		{
			name:       "failure",
			input:      state.AdapterResult{Name: "airbnb-pp-cli", Err: "exec failed"},
			wantStatus: "FAIL",
			wantDetail: "exec failed",
		},
		{
			name:       "skipped takes precedence over pushed=0",
			input:      state.AdapterResult{Name: "ebay-pp-cli", Skipped: true, SkippedReason: "no matching cookies", Pushed: 0},
			wantStatus: "skip",
			wantDetail: "no matching cookies",
		},
		{
			name:       "failure takes precedence over skipped",
			input:      state.AdapterResult{Name: "x", Err: "boom", Skipped: true, SkippedReason: "irrelevant"},
			wantStatus: "FAIL",
			wantDetail: "boom",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotStatus, gotDetail := formatAdapterRow(tc.input)
			if gotStatus != tc.wantStatus {
				t.Errorf("status: got %q, want %q", gotStatus, tc.wantStatus)
			}
			if gotDetail != tc.wantDetail {
				t.Errorf("detail: got %q, want %q", gotDetail, tc.wantDetail)
			}
		})
	}
}
