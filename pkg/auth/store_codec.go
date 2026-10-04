package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// The credential file is decoded by hand so it fails closed: a credential
// store that is only partly understood must never be rewritten from that
// partial understanding (it would silently drop logins).

var errNullValue = errors.New("null value")

// decodeCredentials strictly decodes a whole auth.json. The root must be an
// object of provider objects; truncated or trailing JSON, null, arrays,
// scalars and wrongly typed known fields are errors. Unknown providers and
// unknown fields are kept so rewrites preserve what newer binaries wrote.
func decodeCredentials(data []byte) (map[string]Credential, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if root == nil {
		return nil, fmt.Errorf("credential file: %w", errNullValue)
	}
	out := make(map[string]Credential, len(root))
	for provider, raw := range root {
		var cred Credential
		if err := cred.UnmarshalJSON(raw); err != nil {
			return nil, fmt.Errorf("credential %q: %w", provider, err)
		}
		out[provider] = cred
	}
	return out, nil
}

// UnmarshalJSON decodes one provider record strictly, keeping unknown fields.
func (c *Credential) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if fields == nil {
		return errNullValue
	}
	var out Credential
	for name, raw := range fields {
		var err error
		switch name {
		case "type":
			err = decodeField(raw, &out.Type)
		case "key":
			err = decodeField(raw, &out.Key)
		case "access":
			err = decodeField(raw, &out.Access)
		case "refresh":
			err = decodeField(raw, &out.Refresh)
		case "expires":
			err = decodeField(raw, &out.Expires)
		case "account_id":
			err = decodeField(raw, &out.AccountID)
		case "generation":
			err = decodeField(raw, &out.Generation)
		default:
			if out.extra == nil {
				out.extra = make(map[string]json.RawMessage)
			}
			out.extra[name] = raw
		}
		if err != nil {
			return fmt.Errorf("field %q: %w", name, err)
		}
	}
	*c = out
	return nil
}

// decodeField rejects null, which json.Unmarshal would silently skip.
func decodeField[T string | int64](raw json.RawMessage, dst *T) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errNullValue
	}
	return json.Unmarshal(raw, dst)
}

// MarshalJSON writes the known fields plus any unknown ones read from disk.
func (c Credential) MarshalJSON() ([]byte, error) {
	type plain Credential
	known, err := json.Marshal(plain(c))
	if err != nil || len(c.extra) == 0 {
		return known, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(known, &fields); err != nil {
		return nil, err
	}
	for name, raw := range c.extra {
		if _, ok := fields[name]; !ok {
			fields[name] = raw
		}
	}
	return json.Marshal(fields)
}

// sameCredential compares the fields that identify a stored credential.
// Unknown fields are ignored: rewriting may reformat them.
func sameCredential(a, b Credential) bool {
	return a.Type == b.Type && a.Key == b.Key && a.Access == b.Access && a.Refresh == b.Refresh &&
		a.Expires == b.Expires && a.AccountID == b.AccountID && a.Generation == b.Generation
}

func cloneCredentials(m map[string]Credential) map[string]Credential {
	out := make(map[string]Credential, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
