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
			args: "ship feature X --max 10 --stalled 5 --timeout 2h --verifier haiku --compact 40000 --budget 7.5 --verify-timeout 3m --cwd ../wt-foo",
			want: Command{
				Objective:     "ship feature X",
				MaxIterations: 10,
				MaxStalled:    5,
				Timeout:       2 * time.Hour,
				VerifierSpec:  "haiku",
				CompactAt:     40000,
				TotalBudget:   7.5,
				VerifyTimeout: 3 * time.Minute,
				WorkDir:       "../wt-foo",
			},
		},
		{
			name: "cwd relative",
			args: "do thing --cwd ../wt-foo",
			want: Command{Objective: "do thing", WorkDir: "../wt-foo"},
		},
		{
			name: "cwd absolute",
			args: "do thing --cwd /tmp/wt-foo",
			want: Command{Objective: "do thing", WorkDir: "/tmp/wt-foo"},
		},
		{
			name: "cwd equals form",
			args: "do thing --cwd=/tmp/wt-foo",
			want: Command{Objective: "do thing", WorkDir: "/tmp/wt-foo"},
		},
		{
			name: "cwd last wins",
			args: "do thing --cwd /tmp/a --cwd /tmp/b",
			want: Command{Objective: "do thing", WorkDir: "/tmp/b"},
		},
		{
			name:    "cwd missing value",
			args:    "do thing --cwd",
			wantErr: true,
		},
		{
			name:    "invalid budget",
			args:    "do thing --budget abc",
			wantErr: true,
		},
		{
			name:    "negative budget",
			args:    "do thing --budget -2",
			wantErr: true,
		},
		{
			name:    "invalid verify-timeout",
			args:    "do thing --verify-timeout nope",
			wantErr: true,
		},
		{
			name:    "non-positive verify-timeout",
			args:    "do thing --verify-timeout 0s",
			wantErr: true,
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
			name: "unknown flag",
			args: "do thing --frobnicate 1",
			want: Command{Objective: "do thing --frobnicate 1"},
		},
		{
			name: "unknown flag mid-objective",
			args: "haz que el script corra con --foo y funcione",
			want: Command{Objective: "haz que el script corra con --foo y funcione"},
		},
		{
			name: "known flag mid-objective not tail",
			args: "explica que hace --max 3 en el CLI",
			want: Command{Objective: "explica que hace --max 3 en el CLI", MaxIterations: 0},
		},
		{
			name: "mixed tail",
			args: "fix --foo handling --max 3",
			want: Command{Objective: "fix --foo handling", MaxIterations: 3},
		},
		{
			name: "objective starts with dashes",
			args: "--foo esta roto, arreglalo",
			want: Command{Objective: "--foo esta roto, arreglalo"},
		},
		{
			name:    "known flag final without value after valid",
			args:    "do thing --max 3 --stalled",
			wantErr: true,
		},
		{
			name:    "value looks like flag",
			args:    "do thing --verifier --max",
			wantErr: true,
		},
		{
			name: "duplicate flag last wins",
			args: "do thing --max 3 --max 5",
			want: Command{Objective: "do thing", MaxIterations: 5},
		},
		{
			name: "multiple flags reverse order",
			args: "ship it --budget 7.5 --max 10",
			want: Command{Objective: "ship it", MaxIterations: 10, TotalBudget: 7.5},
		},
		{
			name: "equals form single flag",
			args: "ship it --max=10",
			want: Command{Objective: "ship it", MaxIterations: 10},
		},
		{
			name: "equals form mixed with spaced form",
			args: "ship it --budget=7.5 --max 10",
			want: Command{Objective: "ship it", MaxIterations: 10, TotalBudget: 7.5},
		},
		{
			name: "equals form verifier value with no space",
			args: "do thing --verifier=haiku",
			want: Command{Objective: "do thing", VerifierSpec: "haiku"},
		},
		{
			name:    "equals form invalid value errors",
			args:    "do thing --max=abc",
			wantErr: true,
		},
		{
			name: "equals form mid-objective stays literal",
			args: "explain what --max=3 means",
			want: Command{Objective: "explain what --max=3 means"},
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
