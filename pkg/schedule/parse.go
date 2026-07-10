package schedule

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// CreateArgs is the user-supplied portion of a schedule creation request.
type CreateArgs struct {
	DueAt    time.Time
	TimeZone string
	Text     string
}

// ParseCreateArgs parses either:
//
//	at YYYY-MM-DD HH:MM [IANA-zone] -- text
//	in <duration> -- text
//
// Times without an explicit zone use defaultLocation. Returned due times are
// always UTC. For deterministic relative times, use ParseCreateArgsAt.
func ParseCreateArgs(input string, defaultLocation *time.Location) (CreateArgs, error) {
	return ParseCreateArgsAt(input, time.Now(), defaultLocation)
}

// ParseCreateArgsAt is ParseCreateArgs using now as the reference for relative
// schedules.
func ParseCreateArgsAt(input string, now time.Time, defaultLocation *time.Location) (CreateArgs, error) {
	if defaultLocation == nil {
		return CreateArgs{}, errors.New("default location is required")
	}

	parts := strings.Split(input, "--")
	if len(parts) != 2 {
		return CreateArgs{}, errors.New("schedule must contain exactly one -- separator")
	}
	left := strings.TrimSpace(parts[0])
	text := strings.TrimSpace(parts[1])
	if text == "" {
		return CreateArgs{}, errors.New("schedule text is required")
	}
	fields := strings.Fields(left)
	if len(fields) == 0 {
		return CreateArgs{}, errors.New("schedule time is required")
	}

	switch fields[0] {
	case "at":
		if len(fields) != 3 && len(fields) != 4 {
			return CreateArgs{}, errors.New("at schedules must be: at YYYY-MM-DD HH:MM [IANA-zone] -- text")
		}
		location := defaultLocation
		zoneName := defaultLocation.String()
		if len(fields) == 4 {
			if !isIANAZone(fields[3]) {
				return CreateArgs{}, fmt.Errorf("invalid IANA time zone %q", fields[3])
			}
			var err error
			location, err = time.LoadLocation(fields[3])
			if err != nil {
				return CreateArgs{}, fmt.Errorf("invalid IANA time zone %q: %w", fields[3], err)
			}
			zoneName = fields[3]
		}
		local, err := time.ParseInLocation("2006-01-02 15:04", fields[1]+" "+fields[2], location)
		if err != nil {
			return CreateArgs{}, fmt.Errorf("invalid date and time: %w", err)
		}
		// ParseInLocation normalizes clock times skipped by a DST transition;
		// reject those rather than silently scheduling a different local time.
		if local.Format("2006-01-02 15:04") != fields[1]+" "+fields[2] {
			return CreateArgs{}, errors.New("local time does not exist in the selected time zone")
		}
		return CreateArgs{DueAt: local.UTC(), TimeZone: zoneName, Text: text}, nil

	case "in":
		if len(fields) != 2 {
			return CreateArgs{}, errors.New("relative schedules must be: in <duration> -- text")
		}
		duration, err := time.ParseDuration(fields[1])
		if err != nil || duration <= 0 {
			return CreateArgs{}, errors.New("duration must be a positive Go duration")
		}
		return CreateArgs{
			DueAt:    now.Add(duration).UTC(),
			TimeZone: defaultLocation.String(),
			Text:     text,
		}, nil
	default:
		return CreateArgs{}, errors.New("schedule must start with at or in")
	}
}

func isIANAZone(name string) bool {
	return strings.Contains(name, "/") && !strings.ContainsAny(name, " \\t\n")
}
