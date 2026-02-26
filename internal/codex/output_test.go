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
		{
			name: "mixed text with code fence",
			raw:  "before\n```json\n{\"action\":\"reply\",\"content\":\"ok\"}\n```\nafter",
			want: Decision{Action: "reply", Content: "ok"},
		},
		{
			name: "first valid decision in mixed objects",
			raw:  `prefix {"foo":"bar"} middle {"action":"noop"} suffix {"action":"reply","content":"later"}`,
			want: Decision{Action: "noop"},
		},
		{
			name: "comma after first object",
			raw:  `{"action":"noop"}, {"action":"reply","content":"later"}`,
			want: Decision{Action: "noop"},
		},
		{
			name: "skip invalid decision then parse next",
			raw:  `prefix {"action":"reply"} middle {"action":"reply","content":"ok"} suffix`,
			want: Decision{Action: "reply", Content: "ok"},
		},
		{
			name: "brace in string content",
			raw:  `note {"action":"reply","content":"a { brace } value"} done`,
			want: Decision{Action: "reply", Content: "a { brace } value"},
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
