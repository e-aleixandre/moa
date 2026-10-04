package auth

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// A result for selection A that passed the selection check just before B was
// committed and rejected must not erase B's rejection when it is published.
func TestRecordUse_LateResultCannotEraseNewerSelection(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	s := NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err := s.Set("openai", Credential{Type: "api_key", Key: "selection-a-key"}); err != nil {
		t.Fatal(err)
	}
	a, err := s.PeekSnapshot("openai")
	if err != nil {
		t.Fatal(err)
	}
	checked, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.useSelected = func(key useKey) {
		if key.generation == a.Generation {
			close(checked)
			<-resume
		}
	}
	go func() { s.RecordUse(a, nil); close(done) }()
	select {
	case <-checked:
	case <-time.After(5 * time.Second):
		t.Fatal("A's result never passed the selection check")
	}

	if _, err := s.CommitLogin("openai", a.Generation, Credential{Type: "api_key", Key: "selection-b-key"}); err != nil {
		t.Fatal(err)
	}
	b, err := s.PeekSnapshot("openai")
	if err != nil {
		t.Fatal(err)
	}
	rejection := core.NewProviderCredentialError("openai", core.CredentialSourceStore, "inference", core.CredentialKeyRejected)
	rejection.Generation = b.Generation
	s.RecordUse(b, rejection)
	if st := s.ProviderStatus("openai"); st.State != StatusKeyRejected {
		t.Fatalf("B before A's late result = %+v, want key_rejected", st)
	}

	close(resume)
	<-done
	if st := s.ProviderStatus("openai"); st.State != StatusKeyRejected || !st.NeedsAttention() {
		t.Fatalf("B after A's late result = %+v: an old success erased the current rejection", st)
	}
}
