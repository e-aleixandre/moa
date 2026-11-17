package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

func chainOAuth(name string) Credential {
	return Credential{Type: "oauth", Access: "fake-access-" + name, Refresh: "fake-refresh-" + name, Expires: time.Now().Add(time.Hour).UnixMilli()}
}

// chainStore signs in A and B (as an appended account) and optionally saves a key.
func chainStore(t *testing.T, withKey bool) (s *Store, path, genA, genB string) {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "")
	path = filepath.Join(t.TempDir(), "auth.json")
	s = NewStore(path)
	var err error
	if genA, err = s.CommitLogin("anthropic", "", chainOAuth("A")); err != nil {
		t.Fatal(err)
	}
	if genB, err = s.AddAnthropicAccount(genA, chainOAuth("B")); err != nil {
		t.Fatal(err)
	}
	if withKey {
		if _, err := s.SaveAnthropicAPIKey(genA, "sk-ant-api03-offline-chain-key"); err != nil {
			t.Fatal(err)
		}
	}
	return s, path, genA, genB
}

func chainGens(t *testing.T, s *Store) string {
	t.Helper()
	chain, err := s.AnthropicChain()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(accountGenerations(chain.Accounts), ",")
}

func TestAnthropicChainAddAppendsAndLeavesOthersAlone(t *testing.T) {
	s, path, genA, genB := chainStore(t, true)
	if got := chainGens(t, s); got != genA+","+genB {
		t.Fatalf("chain = %s, want A,B", got)
	}
	disk := readAuthFile(t, path)
	if disk["anthropic"].Generation != genA || disk[anthropicBackupSlot].Key == "" {
		t.Fatalf("adding rewrote A or the key: %+v", disk)
	}
	records := 0
	for name := range disk {
		if strings.HasPrefix(name, anthropicAccountPrefix) {
			records++
		}
	}
	if records != 1 || len(storedOrder(disk)) != 2 {
		t.Fatalf("records=%d order=%v", records, storedOrder(disk))
	}
}

func TestAnthropicChainSingleAccountWritesNothingNew(t *testing.T) {
	s, path, genA, _ := chainStore(t, false)
	if err := s.RemoveAnthropicAccount(mustSecond(t, s)); err != nil {
		t.Fatal(err)
	}
	disk := readAuthFile(t, path)
	for name := range disk {
		if strings.HasPrefix(name, anthropicAccountPrefix) || name == anthropicOrderSlot {
			t.Fatalf("a single account left %q behind", name)
		}
	}
	if disk["anthropic"].Generation != genA {
		t.Fatal("A changed")
	}
}

func mustSecond(t *testing.T, s *Store) string {
	t.Helper()
	chain, err := s.AnthropicChain()
	if err != nil || len(chain.Accounts) < 2 {
		t.Fatalf("chain %+v %v", chain, err)
	}
	return chain.Accounts[1].Generation
}

func TestAnthropicChainReorderNeedsExactPermutationAndMovesNoCredential(t *testing.T) {
	s, path, genA, genB := chainStore(t, false)
	before := readAuthFile(t, path)
	for _, bad := range [][]string{{genB}, {genA, genA}, {genA, genB, "x"}, {genA, "x"}} {
		err := s.ReorderAnthropicAccounts(bad)
		if pe, ok := core.AsProviderCredentialError(err); !ok || pe.Class != core.CredentialChanged {
			t.Fatalf("order %v: err = %v, want credentials_changed", bad, err)
		}
	}
	if err := s.ReorderAnthropicAccounts([]string{genB, genA}); err != nil {
		t.Fatal(err)
	}
	if got := chainGens(t, s); got != genB+","+genA {
		t.Fatalf("chain = %s", got)
	}
	after := readAuthFile(t, path)
	for name, cred := range before {
		if strings.HasPrefix(name, anthropicAccountPrefix) || name == "anthropic" {
			if !sameCredential(cred, after[name]) {
				t.Fatalf("reordering rewrote %s", name)
			}
		}
	}
}

func TestAnthropicChainBadOrderRecordFallsBackToDefaultAndNeverAdmitsForeignRecords(t *testing.T) {
	s, path, genA, genB := chainStore(t, false)
	disk := readAuthFile(t, path)
	disk["openai"] = chainOAuth("openai")
	disk[anthropicOrderSlot] = orderCredential([]string{"openai", "nope", anthropicAccountPrefix + "zz"})
	data, _ := json.Marshal(disk)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := chainGens(t, s); got != genA+","+genB {
		t.Fatalf("chain = %s, want default order without foreign records", got)
	}
	disk[anthropicOrderSlot] = Credential{Type: "account_order"}
	data, _ = json.Marshal(disk)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := chainGens(t, s); got != genA+","+genB {
		t.Fatalf("malformed order: chain = %s", got)
	}
}

func TestAnthropicChainRemovingTheLastAccountMakesTheKeyPrimary(t *testing.T) {
	s, path, genA, genB := chainStore(t, true)
	key := readAuthFile(t, path)[anthropicBackupSlot]
	if err := s.RemoveAnthropicAccount(genB); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveAnthropicAccount(genA); err != nil {
		t.Fatal(err)
	}
	disk := readAuthFile(t, path)
	if _, ok := disk[anthropicBackupSlot]; ok {
		t.Fatal("the key was left in the backup slot")
	}
	primary := disk["anthropic"]
	if primary.Type != "api_key" || primary.Key != key.Key || primary.Generation != key.Generation {
		t.Fatalf("primary = %+v, want the key with its own generation", primary)
	}
	for name := range disk {
		if strings.HasPrefix(name, anthropicAccountPrefix) || name == anthropicOrderSlot {
			t.Fatalf("%s left behind", name)
		}
	}
	if gens := chainGens(t, s); gens != "" {
		t.Fatalf("chain = %q, want none", gens)
	}
	snap, err := s.ResolveSnapshot(context.Background(), "anthropic")
	if err != nil || snap.Kind != "api_key" || snap.Token != key.Key {
		t.Fatalf("resolve = %+v %v", snap, err)
	}
}

func TestAnthropicChainRemovingTheFirstKeepsTheRestAndTheKey(t *testing.T) {
	s, path, genA, genB := chainStore(t, true)
	if err := s.RemoveAnthropicAccount(genA); err != nil {
		t.Fatal(err)
	}
	if got := chainGens(t, s); got != genB {
		t.Fatalf("chain = %q, want B", got)
	}
	if strings.Contains(string(fileBytes(t, path)), "fake-refresh-A") {
		t.Fatal("A's refresh token is still on disk")
	}
	// B is "the plan sign-in" now: status, resolve and the key's CAS follow it.
	snap, err := s.ResolveSnapshot(context.Background(), "anthropic")
	if err != nil || snap.Generation != genB {
		t.Fatalf("resolve = %+v %v, want B", snap, err)
	}
	st := backupStatus(t, s)
	if !st.Active || st.Revision.Primary != genB {
		t.Fatalf("backup = %+v, want active on B", st)
	}
	if _, err := s.SaveAnthropicAPIKey(genB, "sk-ant-api03-offline-replacement"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveAnthropicAccount("unknown"); err == nil {
		t.Fatal("removing an unknown generation succeeded")
	}
}

func TestAnthropicChainKeyAdmissionIsASubsetRule(t *testing.T) {
	s, _, genA, genB := chainStore(t, true)
	st := backupStatus(t, s)
	admit := func(rev BackupRevision) error {
		return s.AdmitAnthropicBackup(context.Background(), rev, func(AnthropicBackupSelection) error { return nil })
	}
	if err := admit(st.Revision); err != nil {
		t.Fatalf("current revision: %v", err)
	}
	// Reordering does not change what was considered.
	if err := s.ReorderAnthropicAccounts([]string{genB, genA}); err != nil {
		t.Fatal(err)
	}
	if err := admit(st.Revision); err != nil {
		t.Fatalf("after reorder: %v", err)
	}
	// A key admitted for A and B stays valid when B is removed ...
	if err := s.RemoveAnthropicAccount(genB); err != nil {
		t.Fatal(err)
	}
	if err := admit(st.Revision); err != nil {
		t.Fatalf("after removing B: %v", err)
	}
	// ... but not when an account nobody considered appears.
	if _, err := s.AddAnthropicAccount(genA, chainOAuth("C")); err != nil {
		t.Fatal(err)
	}
	var calledFor bool
	err := s.AdmitAnthropicBackup(context.Background(), st.Revision, func(AnthropicBackupSelection) error { calledFor = true; return nil })
	if calledFor || err == nil {
		t.Fatalf("a new account was ignored by the admission: %v", err)
	}
}

func TestAnthropicChainReplaceKeepsRecordAndPosition(t *testing.T) {
	s, path, genA, genB := chainStore(t, false)
	before := readAuthFile(t, path)
	gen, err := s.ReplaceAnthropicAccount(genB, chainOAuth("B2"))
	if err != nil {
		t.Fatal(err)
	}
	if got := chainGens(t, s); got != genA+","+gen {
		t.Fatalf("chain = %s", got)
	}
	if _, err := s.ReplaceAnthropicAccount(genB, chainOAuth("B3")); err == nil {
		t.Fatal("the old generation was replaced twice")
	}
	after := readAuthFile(t, path)
	if !sameCredential(before["anthropic"], after["anthropic"]) {
		t.Fatal("replacing B touched A")
	}
}

func TestAnthropicChainAddGivesALegacySignInAnIdentity(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	path := filepath.Join(t.TempDir(), "auth.json")
	seedAuthFile(t, path, map[string]Credential{"anthropic": chainOAuth("legacy")})
	s := NewStore(path)
	if _, err := s.AddAnthropicAccount("", chainOAuth("B")); err != nil {
		t.Fatal(err)
	}
	disk := readAuthFile(t, path)
	if disk["anthropic"].Generation == "" || disk["anthropic"].Access != "fake-access-legacy" {
		t.Fatalf("legacy sign-in = %+v", disk["anthropic"])
	}
	chain, _ := s.AnthropicChain()
	if len(chain.Accounts) != 2 || chain.Accounts[0].Generation == chain.Accounts[1].Generation {
		t.Fatalf("chain = %+v", chain)
	}
}

// A refresh of an account that is not stored under "anthropic" rotates its own
// record and leaves the others alone.
func TestAnthropicChainResolveRefreshesTheRightRecord(t *testing.T) {
	s, path, genA, genB := chainStore(t, false)
	disk := readAuthFile(t, path)
	for name, cred := range disk {
		if strings.HasPrefix(name, anthropicAccountPrefix) {
			cred.Expires = expired()
			disk[name] = cred
		}
	}
	seedAuthFile(t, path, disk)
	var refreshed []string
	s.refresh = func(_ context.Context, provider, refreshToken string) (*OAuthCredentials, error) {
		refreshed = append(refreshed, refreshToken)
		return &OAuthCredentials{Access: "rotated-access", Refresh: "rotated-refresh", Expires: fresh()}, nil
	}
	snap, err := s.ResolveAnthropicAccount(context.Background(), genB)
	if err != nil || snap.Token != "rotated-access" || snap.Generation != genB {
		t.Fatalf("snap = %+v err = %v", snap, err)
	}
	if len(refreshed) != 1 || refreshed[0] != "fake-refresh-B" {
		t.Fatalf("refreshed %v, want only B's refresh token", refreshed)
	}
	after := readAuthFile(t, path)
	if after["anthropic"].Access != "fake-access-A" || after["anthropic"].Generation != genA {
		t.Fatal("refreshing B touched A")
	}
	if _, err := s.ResolveAnthropicAccount(context.Background(), "gone"); err == nil {
		t.Fatal("resolved a generation that is not in the chain")
	} else if pe, ok := core.AsProviderCredentialError(err); !ok || pe.Class != core.CredentialChanged {
		t.Fatalf("err = %v, want credentials_changed", err)
	}
}

func TestAnthropicChainRotationFailureKeepsThePendingRotationOnItsRecord(t *testing.T) {
	s, path, _, genB := chainStore(t, false)
	disk := readAuthFile(t, path)
	for name, cred := range disk {
		if strings.HasPrefix(name, anthropicAccountPrefix) {
			cred.Expires = expired()
			disk[name] = cred
		}
	}
	seedAuthFile(t, path, disk)
	s.refresh = func(context.Context, string, string) (*OAuthCredentials, error) {
		return &OAuthCredentials{Access: "rotated-access", Refresh: "rotated-refresh", Expires: fresh()}, nil
	}
	fail := true
	s.writeFile = func(p string, data []byte) error {
		if fail {
			return errors.Join(errNotWritten, errors.New("disk full"))
		}
		return writeCredentialFile(p, data)
	}
	if _, err := s.ResolveAnthropicAccount(context.Background(), genB); err == nil {
		t.Fatal("a failed save resolved")
	}
	account, _ := findAnthropicAccount(readAuthFile(t, path), genB)
	if !s.hasPending(account.record) || s.hasPending("anthropic") {
		t.Fatalf("pending on %q: %v", account.record, s.pending)
	}
	fail = false
	snap, err := s.ResolveAnthropicAccount(context.Background(), genB)
	if err != nil || snap.Token != "rotated-access" {
		t.Fatalf("after the disk recovered: %+v %v", snap, err)
	}
}

func TestAnthropicChainAccountStatesAndLimits(t *testing.T) {
	s, _, genA, genB := chainStore(t, false)
	now := time.Now()
	s.RememberAnthropicPlanLimit(genA, now.Add(time.Hour))
	got, err := s.AnthropicAccountStatuses(now)
	if err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
	if got[0].State != AccountLimited || !got[0].LimitedUntil.Equal(now.Add(time.Hour)) || got[1].State != AccountAvailable {
		t.Fatalf("states = %+v", got)
	}
	s.RecordUse(CredentialSnapshot{Provider: "anthropic", Source: core.CredentialSourceStore, Kind: "oauth", Generation: genB}, core.NewProviderCredentialError("anthropic", core.CredentialSourceStore, "inference", core.CredentialReconnect))
	if got, _ = s.AnthropicAccountStatuses(now); got[1].State != AccountReconnect {
		t.Fatalf("B = %+v, want reconnect", got[1])
	}
	// An account's limit is its own: a later reset is never shortened, and a limit with no generation is not kept.
	s.RememberAnthropicPlanLimit("", now.Add(time.Hour))
	if _, ok := s.AnthropicPlanReset(""); ok {
		t.Fatal("kept a limit for an unidentified account")
	}
}

func TestAnthropicCodeExchangeReadsTheAccountAddressWhenThereIsOne(t *testing.T) {
	serve := func(body string) *OAuthCredentials {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		defer srv.Close()
		creds, err := exchangeAnthropicCode(context.Background(), srv.Client(), srv.URL, "code", "state", "verifier")
		if err != nil {
			t.Fatal(err)
		}
		return creds
	}
	if got := serve(`{"access_token":"a","refresh_token":"r","expires_in":3600,"account":{"email_address":" ana@example.com "}}`).Email; got != "ana@example.com" {
		t.Fatalf("email = %q", got)
	}
	for name, body := range map[string]string{
		"absent":  `{"access_token":"a","refresh_token":"r","expires_in":3600}`,
		"control": `{"access_token":"a","refresh_token":"r","expires_in":3600,"account":{"email_address":"a\nb"}}`,
		"long":    `{"access_token":"a","refresh_token":"r","expires_in":3600,"account":{"email_address":"` + strings.Repeat("x", 300) + `"}}`,
	} {
		if got := serve(body).Email; got != "" {
			t.Fatalf("%s: email = %q, want none", name, got)
		}
	}
}

// A sign-in that holds no token never serves, but it stays listed so it can
// be reconnected or removed, and it keeps the key from becoming primary
// until it is gone.
func TestAnthropicChainTokenlessAccountIsListedAndRemovable(t *testing.T) {
	s, path, genA, genB := chainStore(t, true)
	disk := readAuthFile(t, path)
	a := disk["anthropic"]
	a.Access, a.Refresh = "", ""
	disk["anthropic"] = a
	seedAuthFile(t, path, disk)
	if got := chainGens(t, s); got != genB {
		t.Fatalf("serving chain = %q, want only B", got)
	}
	statuses, err := s.AnthropicAccountStatuses(time.Now())
	if err != nil || len(statuses) != 2 || statuses[0].Generation != genA || statuses[0].State != AccountReconnect || statuses[1].State != AccountAvailable {
		t.Fatalf("statuses = %+v %v, want the token-less A to reconnect and B available", statuses, err)
	}
	// B alone leaves: the token-less A still holds "anthropic", so the key stays beside it.
	if err := s.RemoveAnthropicAccount(genB); err != nil {
		t.Fatal(err)
	}
	if after := readAuthFile(t, path); after["anthropic"].Generation != genA || after[anthropicBackupSlot].Key == "" {
		t.Fatalf("removing B moved the key over a token-less sign-in: %+v", after)
	}
	if err := s.RemoveAnthropicAccount(genA); err != nil {
		t.Fatalf("a token-less account cannot be removed: %v", err)
	}
	if after := readAuthFile(t, path); after["anthropic"].Type != "api_key" || after[anthropicBackupSlot].Key != "" {
		t.Fatalf("the key did not become primary once the last sign-in went: %+v", after)
	}
}

func TestAnthropicChainTokenlessAccountCanBeReconnected(t *testing.T) {
	s, path, genA, genB := chainStore(t, false)
	disk := readAuthFile(t, path)
	b, _ := findAnthropicAccount(disk, genB)
	broken := disk[b.record]
	broken.Access, broken.Refresh = "", ""
	disk[b.record] = broken
	seedAuthFile(t, path, disk)
	gen, err := s.ReplaceAnthropicAccount(genB, chainOAuth("B2"))
	if err != nil {
		t.Fatalf("reconnecting a token-less account: %v", err)
	}
	if got := chainGens(t, s); got != genA+","+gen {
		t.Fatalf("chain = %q, want A then B's new login", got)
	}
}

// RetrySave persists a pending rotation of any account, and keeps reporting
// the conflict when it cannot.
func TestAnthropicChainRetrySaveReachesALaterAccount(t *testing.T) {
	s, path, genB := pendingOnB(t)
	if err := s.RetrySave("anthropic"); err == nil {
		t.Fatal("retry succeeded while the disk is still failing")
	}
	s.writeFile = writeCredentialFile
	if err := s.RetrySave("anthropic"); err != nil {
		t.Fatalf("retry after the disk recovered: %v", err)
	}
	b, _ := findAnthropicAccount(readAuthFile(t, path), genB)
	if s.hasPending(b.record) || readAuthFile(t, path)[b.record].Refresh != "rotated-refresh" {
		t.Fatal("B's rotation was not saved")
	}
	// A rotation another process superseded cannot be written: still a conflict.
	s2, path2, genB2 := pendingOnB(t)
	disk := readAuthFile(t, path2)
	b2, _ := findAnthropicAccount(disk, genB2)
	cred := disk[b2.record]
	cred.Access, cred.Refresh = "other-access", "other-refresh"
	disk[b2.record] = cred
	seedAuthFile(t, path2, disk)
	s2.writeFile = writeCredentialFile
	if err := s2.RetrySave("anthropic"); err == nil {
		t.Fatal("retry reported success over a conflicting rotation")
	}
}
