// anthropic-accounts.js — which Anthropic plan account a reading is about, in
// words. The ONE place the clients turn an account reference into its label:
// the server names each account (auth.AnthropicAccount.Label: its address, or
// "Account N") in the owner's provider status, and a transcript or a usage
// reading carries only a generation or a place in the chain, never an address.

/** anthropicAccounts — the accounts of the Anthropic row of a provider status
 *  (owner only; a client without the list gets []). */
export function anthropicAccounts(status) {
  const row = (status?.providers || []).find((p) => p?.id === 'anthropic');
  return Array.isArray(row?.accounts) ? row.accounts : [];
}

/** accountLabel — the label of ref ({ generation?, position? }). The
 *  generation names an account for good; the position is what a reading from
 *  before a reorder still has. Without the list: "Account N". */
export function accountLabel(accounts, ref) {
  const list = Array.isArray(accounts) ? accounts : [];
  const byGeneration = ref?.generation ? list.find((a) => a.generation === ref.generation) : null;
  const found = byGeneration || (ref?.position ? list.find((a) => a.position === ref.position) : null);
  if (found?.label) return found.label;
  return ref?.position ? `Account ${ref.position}` : '';
}

/** servingAccount — the account a run is being served by when it is not the
 *  first, from the source its last request reported. null for the first
 *  account, for the key, and for a run that is not running. */
export function servingAccount(execution) {
  const source = execution?.source;
  if (!execution?.phase || source?.kind !== 'oauth' || !(source.account > 1)) return null;
  return { generation: source.primary_generation || '', position: source.account };
}
