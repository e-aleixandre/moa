package goal

import (
	"testing"
	"time"
)

func TestParseCommand(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		want    Command
		wantErr bool
	}{
		{
			name: "objective only",
			args: "empty the backlog",
			want: Command{Objective: "empty the backlog"},
		},
		{
			name: "all knobs",
			args: "ship feature X --max 10 --stalled 5 --timeout 2h --verifier haiku --compact 40000",
			want: Command{
				Objective:     "ship feature X",
				MaxIterations: 10,
				MaxStalled:    5,
				Timeout:       2 * time.Hour,
				VerifierSpec:  "haiku",
				CompactAt:     40000,
			},
		},
		{
			name: "objective with punctuation before flags",
			args: `create HOLA.txt with "hola" --max 3`,
			want: Command{Objective: `create HOLA.txt with "hola"`, MaxIterations: 3},
		},
		{
			name:    "empty",
			args:    "",
			wantErr: true,
		},
		{
			name:    "flags but no objective",
			args:    "--max 3",
			wantErr: true,
		},
		{
			name:    "flag missing value",
			args:    "do thing --max",
			wantErr: true,
		},
		{
			name:    "invalid int",
			args:    "do thing --max abc",
			wantErr: true,
		},
		{
			name:    "negative int",
			args:    "do thing --compact -5",
			wantErr: true,
		},
		{
			name:    "invalid duration",
			args:    "do thing --timeout nope",
			wantErr: true,
		},
		{
			name:    "unknown flag",
			args:    "do thing --frobnicate 1",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCommand(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
