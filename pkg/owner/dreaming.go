package owner

import "time"

// Dreaming is the per-owner switch and schedule of the nightly consolidation.
// It is off unless "enabled" is explicitly true, like the heartbeat: the night
// rewrites the book on its own, so it runs only where the user asked for it.
type Dreaming struct {
	Enabled *bool `json:"enabled,omitempty"`
	// Hour is the local hour (0-23) after which the night may run; default 3.
	Hour *int `json:"hour,omitempty"`
	// TZ is an IANA zone; empty is UTC. It is never assumed from the server.
	TZ string `json:"tz,omitempty"`
	// BudgetUSD caps one night at catalog prices; default DefaultDreamingBudgetUSD.
	BudgetUSD float64 `json:"budget_usd,omitempty"`
}

// DefaultDreamingBudgetUSD is the default cap of one night.
const DefaultDreamingBudgetUSD = 3.0

// DreamingSettings is the resolved configuration.
type DreamingSettings struct {
	Enabled   bool
	Hour      int
	Location  *time.Location
	BudgetUSD float64
}

// DreamingSettings resolves the owner's configuration. An unknown zone keeps
// the night off rather than guessing one.
func (o Owner) DreamingSettings() DreamingSettings {
	s := DreamingSettings{Hour: 3, Location: time.UTC, BudgetUSD: DefaultDreamingBudgetUSD}
	d := o.Dreaming
	if d == nil {
		return s
	}
	if d.Hour != nil && *d.Hour >= 0 && *d.Hour <= 23 {
		s.Hour = *d.Hour
	}
	if d.BudgetUSD > 0 {
		s.BudgetUSD = d.BudgetUSD
	}
	if d.TZ != "" {
		loc, err := time.LoadLocation(d.TZ)
		if err != nil {
			return s
		}
		s.Location = loc
	}
	s.Enabled = d.Enabled != nil && *d.Enabled
	return s
}

// DreamingDue reports the civil date a night should run for at now, or ""
// when it is not yet the hour (or the owner has it off). Whether that date
// already ran is the dreaming's own record, not this function's.
func (s DreamingSettings) DreamingDue(now time.Time) string {
	if !s.Enabled {
		return ""
	}
	local := now.In(s.Location)
	if local.Hour() < s.Hour {
		return ""
	}
	return local.Format("2006-01-02")
}
