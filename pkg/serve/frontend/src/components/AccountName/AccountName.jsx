import "./AccountName.css";
import { accountLabel, anthropicAccounts } from "../../data/anthropic-accounts.js";
import { useProviderStatus } from "../../data/providers.js";

// AccountName — an Anthropic plan account in words, cut by CSS when it does not
// fit and whole in the title. The text is never truncated in code: the label
// is the server's, and the width is the layout's to decide.
export function AccountName({ account, class: className = "" }) {
  const status = useProviderStatus();
  const label = accountLabel(anthropicAccounts(status), account);
  if (!label) return null;
  return <span class={`acct-name ${className}`.trim()} title={label}>{label}</span>;
}
