package serve

import (
	"net/http"

	"github.com/e-aleixandre/moa/pkg/auth"
)

func (pc *providerCredentials) backupKey(w http.ResponseWriter, r *http.Request, provider string) {
	var body struct {
		Expected *auth.BackupRevision `json:"expected_revision"`
		Key      string               `json:"key"`
	}
	if !decodeProviderBody(w, r, provider, &body) {
		return
	}
	if provider != "anthropic" || body.Expected == nil {
		writeProviderError(w, 400, provider, "invalid_request")
		return
	}
	_, err := pc.store.SaveAnthropicBackup(*body.Expected, body.Key)
	pc.backupResult(w, provider, err)
}

func (pc *providerCredentials) backupEnabled(w http.ResponseWriter, r *http.Request, provider string) {
	var body struct {
		Expected *auth.BackupRevision `json:"expected_revision"`
		Enabled  *bool                `json:"enabled"`
	}
	if !decodeProviderBody(w, r, provider, &body) {
		return
	}
	if provider != "anthropic" || body.Expected == nil || body.Enabled == nil {
		writeProviderError(w, 400, provider, "invalid_request")
		return
	}
	_, err := pc.store.SetAnthropicBackupEnabled(*body.Expected, *body.Enabled)
	pc.backupResult(w, provider, err)
}

func (pc *providerCredentials) backupRemove(w http.ResponseWriter, r *http.Request, provider string) {
	var body struct {
		Expected *auth.BackupRevision `json:"expected_revision"`
	}
	if !decodeProviderBody(w, r, provider, &body) {
		return
	}
	if provider != "anthropic" || body.Expected == nil {
		writeProviderError(w, 400, provider, "invalid_request")
		return
	}
	_, err := pc.store.RemoveAnthropicBackup(*body.Expected)
	pc.backupResult(w, provider, err)
}

func (pc *providerCredentials) backupResult(w http.ResponseWriter, provider string, err error) {
	if err != nil {
		writeProviderFailure(w, provider, "backup", err)
		return
	}
	writeJSON(w, http.StatusOK, pc.row(pc.store.ProviderStatus(provider), true, true))
}
