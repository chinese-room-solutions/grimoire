package kernel

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncodeRequest(t *testing.T) {
	got := encodeRequest("r7", "echo \"hi\"\nx=5")
	want := "r7\n" + base64.StdEncoding.EncodeToString([]byte("echo \"hi\"\nx=5")) + "\n"
	require.Equal(t, want, string(got))
}

func TestParseEvent(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		want    Event
		wantErr bool
	}{
		{
			name: "output",
			line: `{"id":"r7","type":"output","data":"hello\n"}`,
			want: Event{Id: "r7", Type: EventOutput, Data: "hello\n"},
		},
		{
			name: "output with unicode",
			line: `{"id":"r1","type":"output","data":"warning… café"}`,
			want: Event{Id: "r1", Type: EventOutput, Data: "warning… café"},
		},
		{
			name: "exit carries code and duration",
			line: `{"id":"r2","type":"exit","code":1,"dur_ms":12}`,
			want: Event{Id: "r2", Type: EventExit, Code: 1, DurMS: 12},
		},
		{
			name: "error is terminal",
			line: `{"id":"r3","type":"error","data":"boom"}`,
			want: Event{Id: "r3", Type: EventError, Data: "boom"},
		},
		{
			name:    "unknown type rejected",
			line:    `{"id":"r4","type":"banana"}`,
			wantErr: true,
		},
		{
			name:    "malformed json rejected",
			line:    `{not json`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseEvent([]byte(tt.line))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestEventTerminal(t *testing.T) {
	require.False(t, Event{Type: EventOutput}.Terminal())
	require.True(t, Event{Type: EventExit}.Terminal())
	require.True(t, Event{Type: EventError}.Terminal())
}
