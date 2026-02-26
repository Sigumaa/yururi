package codex

import "testing"

func TestParseDecisionOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    Decision
		wantErr bool
	}{
		{
			name: "noop",
			raw:  `{"action":"noop"}`,
			want: Decision{Action: "noop"},
		},
		{
			name: "reply",
			raw:  `{"action":"reply","content":"こんにちは"}`,
			want: Decision{Action: "reply", Content: "こんにちは"},
		},
		{
			name:    "reply missing content",
			raw:     `{"action":"reply"}`,
			wantErr: true,
		},
		{
			name:    "unsupported action",
			raw:     `{"action":"send","content":"x"}`,
			wantErr: true,
		},
		{
			name:    "invalid json",
			raw:     `not-json`,
			wantErr: true,
		},
		{
			name: "wrapped text",
			raw:  "```json\n{\"action\":\"noop\"}\n```",
			want: Decision{Action: "noop"},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseDecisionOutput(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseDecisionOutput() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDecisionOutput() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("ParseDecisionOutput() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
