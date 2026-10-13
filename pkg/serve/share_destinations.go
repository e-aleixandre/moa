package serve

import (
	"net/http"
	"path/filepath"
	"sort"
	"time"

	"github.com/e-aleixandre/moa/pkg/session"
)

// maxShareSessions caps the plain sessions of /api/share/destinations: the
// full roster runs to hundreds of entries, too heavy for a share sheet.
const maxShareSessions = 50

// shareDestination is one place a native client can drop something shared into
// moa. Clients must ignore unknown fields and kinds.
type shareDestination struct {
	Kind      string       `json:"kind"` // "owner" | "session"
	SessionID string       `json:"session_id"`
	Title     string       `json:"title"`
	Project   string       `json:"project"`
	State     SessionState `json:"state"`
	Updated   time.Time    `json:"updated"`
}

// shareDestinations lists the owners with a conversation (most recent first)
// followed by the user's own most recent sessions. Owner-launched children and
// the owner conversations themselves never appear as plain sessions.
func (m *Manager) shareDestinations() ([]shareDestination, error) {
	owners, err := m.ListOwners()
	if err != nil {
		return nil, err
	}
	roster := m.ListWith(ListOptions{IncludeOwners: true})
	byID := make(map[string]SessionInfo, len(roster))
	for _, info := range roster {
		byID[info.ID] = info
	}

	out := make([]shareDestination, 0, len(owners)+maxShareSessions)
	for _, own := range owners {
		if own.SessionID == "" {
			continue
		}
		out = append(out, shareDestination{
			Kind:      "owner",
			SessionID: own.SessionID,
			Title:     own.Name,
			Project:   filepath.Base(own.Root),
			State:     own.SessionState,
			Updated:   byID[own.SessionID].Updated,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })

	// roster is already ordered most recent first.
	sessions := 0
	for _, info := range roster {
		if sessions == maxShareSessions {
			break
		}
		if info.Kind == session.KindOwner || info.Origin == "owner" {
			continue
		}
		project := ""
		if info.CWD != "" {
			project = filepath.Base(info.CWD)
		}
		out = append(out, shareDestination{
			Kind:      "session",
			SessionID: info.ID,
			Title:     info.Title,
			Project:   project,
			State:     info.State,
			Updated:   info.Updated,
		})
		sessions++
	}
	return out, nil
}

func handleShareDestinations(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		destinations, err := mgr.shareDestinations()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"destinations": destinations})
	}
}
