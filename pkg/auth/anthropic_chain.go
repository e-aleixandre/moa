package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Several Anthropic plan sign-ins are spent in a chain: the first that is not
// known to be exhausted serves, and the API key beside them comes last.
//
// On disk "anthropic" stays the first account, so a store with one sign-in is
// byte-for-byte what it was. Every further account lives in its own record,
// named once and never renamed, and an order record lists the accounts by
// priority. Reordering rewrites only that record, so a rotation another
// process keeps pending for an account is never orphaned by a move.
const (
	anthropicAccountPrefix = "__anthropic_account_v1_"
	anthropicOrderSlot     = "__anthropic_accounts_v1"
)

// AnthropicAccount is one plan sign-in of the chain. Generation is its
// identity: the record only says where it is stored. It holds no secret.
type AnthropicAccount struct {
	Generation string
	Email      string
	// Position is 1-based, in priority order.
	Position int
	// Unusable marks a sign-in that holds no token: it never serves.
	Unusable bool
	record   string
}

// AnthropicChain is the plan sign-ins in priority order. Env reports that an
// environment credential manages Anthropic, which has no chain.
type AnthropicChain struct {
	Accounts []AnthropicAccount
	Env      bool
}

// Label names the account in every place it is shown (Providers, the status
// line, the usage widget): its address, or "Account N" when it reported none.
// The clients truncate it by CSS; it is never cut here.
func (a AnthropicAccount) Label() string {
	if a.Email != "" {
		return a.Email
	}
	return "Account " + strconv.Itoa(a.Position)
}

func isAnthropicAccountRecord(name string) bool {
	return name == "anthropic" || strings.HasPrefix(name, anthropicAccountPrefix)
}

// storedOrder returns the records listed by the order record, or nil when it
// is absent or malformed. A bad order record is no credential: at worst the
// priority is the default one, and the key stays last either way.
func storedOrder(disk map[string]Credential) []string {
	rec, ok := disk[anthropicOrderSlot]
	if !ok || rec.Type != "account_order" {
		return nil
	}
	var order []string
	if err := json.Unmarshal(rec.extra["order"], &order); err != nil {
		return nil
	}
	return order
}

func orderCredential(records []string) Credential {
	raw, _ := json.Marshal(records)
	return Credential{Type: "account_order", extra: map[string]json.RawMessage{"order": raw}}
}

// anthropicAccounts normalizes disk into the chain, without writing: the
// ordered records that hold a usable plan sign-in, then any account that the
// order does not mention ("anthropic" first, the others by name).
func anthropicAccounts(disk map[string]Credential) []AnthropicAccount {
	return listAnthropicAccounts(disk, false)
}

// anthropicAdminAccounts is the chain as Providers administers it: the same
// order, plus the sign-ins that hold no token at all. They never serve, but
// they must stay visible so they can be reconnected or removed.
func anthropicAdminAccounts(disk map[string]Credential) []AnthropicAccount {
	return listAnthropicAccounts(disk, true)
}

func listAnthropicAccounts(disk map[string]Credential, withUnusable bool) []AnthropicAccount {
	var out []AnthropicAccount
	seenRecord, seenGeneration := map[string]bool{}, map[string]bool{}
	add := func(record string) {
		cred, ok := disk[record]
		if seenRecord[record] || !isAnthropicAccountRecord(record) || !ok || cred.Type != "oauth" {
			return
		}
		unusable := checkStored("anthropic", cred) != nil
		if unusable && !withUnusable {
			return
		}
		seenRecord[record] = true
		if seenGeneration[cred.Generation] {
			return
		}
		seenGeneration[cred.Generation] = true
		out = append(out, AnthropicAccount{Generation: cred.Generation, Email: cred.Email, Position: len(out) + 1, record: record, Unusable: unusable})
	}
	for _, record := range storedOrder(disk) {
		add(record)
	}
	add("anthropic")
	var rest []string
	for record := range disk {
		if strings.HasPrefix(record, anthropicAccountPrefix) {
			rest = append(rest, record)
		}
	}
	sort.Strings(rest)
	for _, record := range rest {
		add(record)
	}
	return out
}

func accountRecords(accounts []AnthropicAccount) []string {
	out := make([]string, len(accounts))
	for i, a := range accounts {
		out[i] = a.record
	}
	return out
}

func findAnthropicAccount(disk map[string]Credential, generation string) (AnthropicAccount, bool) {
	return findIn(anthropicAccounts(disk), generation)
}

// findAnthropicAdminAccount also finds a sign-in without tokens.
func findAnthropicAdminAccount(disk map[string]Credential, generation string) (AnthropicAccount, bool) {
	return findIn(anthropicAdminAccounts(disk), generation)
}

func findIn(accounts []AnthropicAccount, generation string) (AnthropicAccount, bool) {
	for _, a := range accounts {
		if a.Generation == generation {
			return a, true
		}
	}
	return AnthropicAccount{}, false
}

// anthropicLeadRecord is the record that stands for "the plan sign-in" where
// only one is meant (status, text selection of the provider, the key's CAS):
// "anthropic" while it is an account, else the first account.
func anthropicLeadRecord(disk map[string]Credential) string {
	accounts := anthropicAccounts(disk)
	for _, a := range accounts {
		if a.record == "anthropic" {
			return "anthropic"
		}
	}
	if len(accounts) > 0 {
		return accounts[0].record
	}
	return "anthropic"
}

// providerRecord is the record the default selection of provider reads.
func providerRecord(disk map[string]Credential, provider string) string {
	if provider == "anthropic" {
		return anthropicLeadRecord(disk)
	}
	return provider
}

func accountGenerations(accounts []AnthropicAccount) []string {
	out := make([]string, len(accounts))
	for i, a := range accounts {
		out[i] = a.Generation
	}
	return out
}

// chainPending reports an unsaved rotation or replacement fence on any record
// of the chain.
func (s *Store) chainPending(disk map[string]Credential) bool {
	if s.hasPending("anthropic") {
		return true
	}
	for _, a := range anthropicAccounts(disk) {
		if s.hasPending(a.record) {
			return true
		}
	}
	return false
}

// AnthropicAdminChain reads the accounts Providers lists, sign-ins without
// tokens included. The chain that serves requests is AnthropicChain.
func (s *Store) AnthropicAdminChain() (AnthropicChain, error) {
	disk, err := s.reload()
	if err != nil {
		return AnthropicChain{}, storeUnavailable("anthropic", "chain")
	}
	return AnthropicChain{Accounts: anthropicAdminAccounts(disk), Env: envManaged("anthropic")}, nil
}

// AnthropicChain reads the chain from disk.
func (s *Store) AnthropicChain() (AnthropicChain, error) {
	disk, err := s.reload()
	if err != nil {
		return AnthropicChain{}, storeUnavailable("anthropic", "chain")
	}
	return AnthropicChain{Accounts: anthropicAccounts(disk), Env: envManaged("anthropic")}, nil
}

// AnthropicLeadGeneration is the generation Providers shows for the plan row:
// the one of "anthropic", else of the first account ("" for none).
func (s *Store) AnthropicLeadGeneration() (string, error) {
	disk, err := s.reload()
	if err != nil {
		return "", storeUnavailable("anthropic", "resolve")
	}
	return disk[anthropicLeadRecord(disk)].Generation, nil
}

// Account states shown in Providers.
const (
	AccountAvailable  = "available"
	AccountLimited    = "limited"
	AccountReconnect  = "reconnect"
	AccountRenewOnUse = "renew_on_use"
	AccountSaveFailed = "save_failed"
)

// AnthropicAccountStatus is one account of the chain with what this process
// knows about it.
type AnthropicAccountStatus struct {
	AnthropicAccount
	State        string
	LimitedUntil time.Time
}

// AnthropicAccountStatuses reports every account in priority order. It reads
// disk and memory only: no refresh, no write, no request.
func (s *Store) AnthropicAccountStatuses(now time.Time) ([]AnthropicAccountStatus, error) {
	disk, err := s.reload()
	if err != nil {
		return nil, storeUnavailable("anthropic", "chain")
	}
	var out []AnthropicAccountStatus
	for _, a := range anthropicAdminAccounts(disk) {
		st := AnthropicAccountStatus{AnthropicAccount: a, State: AccountAvailable}
		rec, seen := s.observed(useKey{"anthropic", core.CredentialSourceStore, a.Generation})
		until, limited := s.AnthropicPlanLimit(a.Generation, now)
		switch {
		case s.hasPending(a.record):
			st.State = AccountSaveFailed
		case a.Unusable:
			st.State = AccountReconnect
		case seen && rec.state == StatusReconnect:
			st.State = AccountReconnect
		case limited:
			st.State, st.LimitedUntil = AccountLimited, until
		case credentialExpired(disk[a.record]):
			st.State = AccountRenewOnUse
		}
		out = append(out, st)
	}
	return out, nil
}

// ResolveAnthropicAccount is ResolveSnapshot for the account with generation:
// an expired token is rotated on demand, and an account that is gone (removed
// or signed in again) is credentials_changed, never another account.
func (s *Store) ResolveAnthropicAccount(ctx context.Context, generation string) (CredentialSnapshot, error) {
	if envManaged("anthropic") {
		return CredentialSnapshot{}, credentialsChanged("anthropic", "resolve", generation)
	}
	return s.resolveStored(ctx, "anthropic", SelectDefault, func(disk map[string]Credential) (string, error) {
		a, ok := findAnthropicAccount(disk, generation)
		if !ok {
			return "", credentialsChanged("anthropic", "resolve", generation)
		}
		return a.record, nil
	})
}

// AdmitAnthropicAccount linearizes a dispatch with the account holding
// generation under the Store locks. Its position is not checked: a reorder
// between the selection and the send does not fail the send. admit gets the
// account's current position. The callback is a short gate, never HTTP.
func (s *Store) AdmitAnthropicAccount(ctx context.Context, generation string, admit func(snap CredentialSnapshot, position int) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if envManaged("anthropic") {
		return credentialsChanged("anthropic", "dispatch", generation)
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	return s.transact("anthropic", "dispatch", func(disk map[string]Credential) (bool, *pendingRotation, error) {
		if err := ctx.Err(); err != nil {
			return false, nil, err
		}
		a, ok := findAnthropicAccount(disk, generation)
		if !ok || s.hasPending(a.record) || credentialExpired(disk[a.record]) {
			return false, nil, credentialsChanged("anthropic", "dispatch", generation)
		}
		return false, nil, admit(storeSnapshotAt("anthropic", a.record, disk[a.record]), a.Position)
	})
}

func newAccountRecord(disk map[string]Credential) (string, error) {
	for range 8 {
		buf := make([]byte, 4)
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		record := anthropicAccountPrefix + hex.EncodeToString(buf)
		if _, taken := disk[record]; !taken {
			return record, nil
		}
	}
	return "", errors.New("no free account record")
}

// saveFencedMany writes disk with every record fenced until the write is
// known durable, as a login does. A failure before the rename lifts the
// fences: the file still holds what it held.
func (s *Store) saveFencedMany(provider, op string, disk map[string]Credential, records ...string) error {
	s.mu.Lock()
	prev := make(map[string]pendingRotation, len(records))
	for _, record := range records {
		if p, ok := s.pending[record]; ok {
			prev[record] = p
		}
		s.pending[record] = pendingRotation{fence: true}
	}
	s.mu.Unlock()
	if err := s.saveLocked(disk); err != nil {
		if errors.Is(err, errNotWritten) {
			s.mu.Lock()
			for _, record := range records {
				delete(s.pending, record)
				if p, ok := prev[record]; ok {
					s.pending[record] = p
				}
			}
			s.mu.Unlock()
		}
		return persistenceFailed(provider, op)
	}
	for _, record := range records {
		s.dropPending(record)
	}
	return nil
}

// AddAnthropicAccount stores cred as a new login after the existing accounts.
// Without any account it is the ordinary login of "anthropic" (expectedLead is
// its CAS, and an API key there moves beside it). Otherwise the account is
// appended whatever else changed, so adding is commutative; the API key and
// the other accounts are untouched. It returns the new generation.
func (s *Store) AddAnthropicAccount(expectedLead string, cred Credential) (string, error) {
	disk, err := s.reload()
	if err != nil {
		return "", storeUnavailable("anthropic", "login")
	}
	if len(anthropicAccounts(disk)) == 0 {
		return s.CommitLogin("anthropic", expectedLead, cred)
	}
	gen, err := newGeneration()
	if err != nil {
		return "", persistenceFailed("anthropic", "login")
	}
	cred.Generation, cred.extra = gen, nil
	s.refreshMu.Lock()
	err = s.transact("anthropic", "login", func(disk map[string]Credential) (bool, *pendingRotation, error) {
		accounts := anthropicAdminAccounts(disk)
		if len(anthropicAccounts(disk)) == 0 {
			return false, nil, credentialsChanged("anthropic", "login", "")
		}
		record, err := newAccountRecord(disk)
		if err != nil {
			return false, nil, persistenceFailed("anthropic", "login")
		}
		fenced := []string{record}
		// A legacy sign-in has no identity yet: give it one without replacing it.
		for _, a := range accounts {
			if a.Generation != "" {
				continue
			}
			legacy := disk[a.record]
			if legacy.Generation, err = newGeneration(); err != nil {
				return false, nil, persistenceFailed("anthropic", "login")
			}
			disk[a.record] = legacy
			fenced = append(fenced, a.record)
		}
		disk[record] = cred
		disk[anthropicOrderSlot] = orderCredential(append(accountRecords(accounts), record))
		return false, nil, s.saveFencedMany("anthropic", "login", disk, fenced...)
	})
	s.refreshMu.Unlock()
	if err != nil {
		return "", err
	}
	s.observe(useKey{"anthropic", core.CredentialSourceStore, gen}, StatusSaved)
	return gen, nil
}

// ReplaceAnthropicAccount signs the account holding expectedGeneration in
// again: a new login in the same record and position, superseding any unsaved
// rotation of the old one. It returns the new generation.
func (s *Store) ReplaceAnthropicAccount(expectedGeneration string, cred Credential) (string, error) {
	gen, err := newGeneration()
	if err != nil {
		return "", persistenceFailed("anthropic", "login")
	}
	cred.Generation, cred.extra = gen, nil
	s.refreshMu.Lock()
	err = s.transact("anthropic", "login", func(disk map[string]Credential) (bool, *pendingRotation, error) {
		a, ok := findAnthropicAdminAccount(disk, expectedGeneration)
		if !ok {
			return false, nil, credentialsChanged("anthropic", "login", expectedGeneration)
		}
		disk[a.record] = cred
		return false, nil, s.saveFencedMany("anthropic", "login", disk, a.record)
	})
	s.refreshMu.Unlock()
	if err != nil {
		return "", err
	}
	s.observe(useKey{"anthropic", core.CredentialSourceStore, gen}, StatusSaved)
	return gen, nil
}

// ReorderAnthropicAccounts sets the priority. order must name exactly the
// current accounts by generation, or nothing is written (credentials_changed):
// a page that missed an add or a remove cannot undo it.
func (s *Store) ReorderAnthropicAccounts(order []string) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	return s.transact("anthropic", "accounts_order", func(disk map[string]Credential) (bool, *pendingRotation, error) {
		accounts := anthropicAdminAccounts(disk)
		if len(order) != len(accounts) {
			return false, nil, credentialsChanged("anthropic", "accounts_order", "")
		}
		byGeneration := map[string]AnthropicAccount{}
		for _, a := range accounts {
			byGeneration[a.Generation] = a
		}
		records := make([]string, 0, len(order))
		seen := map[string]bool{}
		for _, gen := range order {
			a, ok := byGeneration[gen]
			if !ok || seen[gen] {
				return false, nil, credentialsChanged("anthropic", "accounts_order", "")
			}
			seen[gen] = true
			records = append(records, a.record)
		}
		if len(accounts) < 2 || strings.Join(records, "\x00") == strings.Join(accountRecords(accounts), "\x00") {
			return false, nil, nil
		}
		disk[anthropicOrderSlot] = orderCredential(records)
		return false, nil, s.saveFencedMany("anthropic", "accounts_order", disk, anthropicOrderSlot)
	})
}

// RemoveAnthropicAccount deletes the account holding generation, which stops
// being used from the next request. The API key stays; when it was the last
// account the key becomes the primary credential, as a key alone always is.
func (s *Store) RemoveAnthropicAccount(generation string) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	return s.transact("anthropic", "account_remove", func(disk map[string]Credential) (bool, *pendingRotation, error) {
		accounts := anthropicAdminAccounts(disk)
		var gone AnthropicAccount
		var rest []string
		found := false
		for _, a := range accounts {
			if a.Generation == generation && !found {
				gone, found = a, true
				continue
			}
			rest = append(rest, a.record)
		}
		if !found {
			return false, nil, credentialsChanged("anthropic", "account_remove", generation)
		}
		delete(disk, gone.record)
		fenced := []string{gone.record}
		if len(rest) >= 2 {
			disk[anthropicOrderSlot] = orderCredential(rest)
			fenced = append(fenced, anthropicOrderSlot)
		} else if _, ok := disk[anthropicOrderSlot]; ok {
			delete(disk, anthropicOrderSlot)
			fenced = append(fenced, anthropicOrderSlot)
		}
		if len(rest) == 0 {
			// A moved key keeps its generation: a key moves, it is not a rotation.
			if key, ok := disk[anthropicBackupSlot]; ok && key.Type == "api_key" && key.Key != "" {
				if _, taken := disk["anthropic"]; !taken {
					disk["anthropic"] = key
					delete(disk, anthropicBackupSlot)
					fenced = append(fenced, "anthropic", anthropicBackupSlot)
				}
			}
		}
		return false, nil, s.saveFencedMany("anthropic", "account_remove", disk, fenced...)
	})
}

// noteAnthropicServed remembers which account answered last. It lives for the
// process: after a restart the widget starts from the first account that is
// not known to be exhausted, until a response says otherwise.
func (s *Store) noteAnthropicServed(generation string) {
	s.useMu.Lock()
	s.servedGeneration = generation
	s.useMu.Unlock()
}

// ServingAnthropicAccount is the account the plan is being spent on: the one
// of the last good response while it is still in the chain, else the first
// account with no remembered limit, else the first. ok is false without any
// usable account, or when the environment manages Anthropic.
func (s *Store) ServingAnthropicAccount(now time.Time) (AnthropicAccount, bool) {
	chain, err := s.AnthropicChain()
	if err != nil || chain.Env || len(chain.Accounts) == 0 {
		return AnthropicAccount{}, false
	}
	s.useMu.Lock()
	served := s.servedGeneration
	s.useMu.Unlock()
	if served != "" {
		if a, ok := findIn(chain.Accounts, served); ok {
			return a, true
		}
	}
	for _, a := range chain.Accounts {
		if _, limited := s.AnthropicPlanLimit(a.Generation, now); !limited {
			return a, true
		}
	}
	return chain.Accounts[0], true
}

// PeekAnthropicAccountToken is PeekOAuthToken for one account of the chain.
// It never refreshes: an expired or unsaved token reports valid=false and a
// real request renews it.
func (s *Store) PeekAnthropicAccountToken(generation string) (token string, found, valid bool) {
	disk, err := s.reload()
	if err != nil {
		return "", false, false
	}
	a, ok := findAnthropicAccount(disk, generation)
	if !ok || envManaged("anthropic") {
		return "", false, false
	}
	cred := disk[a.record]
	if s.hasPending(a.record) || credentialExpired(cred) || cred.Access == "" {
		return "", true, false
	}
	return cred.Access, true, true
}
