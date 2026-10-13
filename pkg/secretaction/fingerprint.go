package secretaction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"path"
	"strings"
)

// CommandFingerprint identifies a command action: hex sha256 over the
// canonical JSON of {kind, command, cwd, host}. The phone compares it with the
// fingerprint it saved a value under to tell "exactly the same action".
func CommandFingerprint(command, cwd, host string) string {
	return fingerprint(struct {
		Command string `json:"command"`
		Cwd     string `json:"cwd"`
		Host    string `json:"host"`
		Kind    string `json:"kind"`
	}{command, cwd, host, KindCommand})
}

// BrowserFingerprint identifies a browser fill action: hex sha256 over the
// canonical JSON of {kind, origin, fields:[{realLabel, realType, kind}],
// submit}. Only what the server observed counts, not the model's words.
func BrowserFingerprint(origin string, fields []Field, submit bool) string {
	type fp struct {
		Kind      string `json:"kind"`
		RealLabel string `json:"realLabel"`
		RealType  string `json:"realType"`
	}
	fs := make([]fp, len(fields))
	for i, f := range fields {
		fs[i] = fp{Kind: f.Kind, RealLabel: f.RealLabel, RealType: f.RealType}
	}
	return fingerprint(struct {
		Fields []fp   `json:"fields"`
		Kind   string `json:"kind"`
		Origin string `json:"origin"`
		Submit bool   `json:"submit"`
	}{fs, KindBrowser, origin, submit})
}

func fingerprint(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// sshOptsWithArg are the ssh(1) flags that consume the next word.
const sshOptsWithArg = "BbcDEeFIiJLlmOoPpQRSWw"

// ParseSSHHost returns the host the first `ssh` invocation in command
// connects to (without user or port), or "". Best effort, for the card only:
// it splits words on whitespace and shell separators and understands quotes
// just enough to skip quoted arguments.
func ParseSSHHost(command string) string {
	words := shellWords(command)
	for i := 0; i < len(words); i++ {
		if path.Base(words[i]) != "ssh" {
			continue
		}
		// Only a word in command position: start, or after a separator.
		if i > 0 && !isSeparator(words[i-1]) {
			continue
		}
		for j := i + 1; j < len(words); j++ {
			w := words[j]
			if isSeparator(w) {
				break
			}
			if w == "--" {
				continue
			}
			if strings.HasPrefix(w, "-") && len(w) > 1 {
				// "-p 22" takes the next word; "-p22" and "-tt" do not.
				last := w[len(w)-1]
				if len(w) == 2 && strings.IndexByte(sshOptsWithArg, last) >= 0 {
					j++
				}
				continue
			}
			return sshDestinationHost(w)
		}
	}
	return ""
}

func sshDestinationHost(dest string) string {
	if strings.HasPrefix(dest, "ssh://") {
		u, err := url.Parse(dest)
		if err != nil {
			return ""
		}
		return u.Hostname()
	}
	if at := strings.LastIndexByte(dest, '@'); at >= 0 {
		dest = dest[at+1:]
	}
	return dest
}

func isSeparator(w string) bool {
	switch w {
	case ";", "&&", "||", "|", "&", "(", ")":
		return true
	}
	return false
}

// shellWords splits command into words and separator tokens.
func shellWords(command string) []string {
	var words []string
	var cur strings.Builder
	inWord := false
	flush := func() {
		if inWord {
			words = append(words, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case c == '\'' || c == '"':
			end := strings.IndexByte(command[i+1:], c)
			if end < 0 {
				cur.WriteString(command[i+1:])
				i = len(command)
			} else {
				cur.WriteString(command[i+1 : i+1+end])
				i += end + 1
			}
			inWord = true
		case c == ' ' || c == '\t' || c == '\n':
			flush()
		case c == ';' || c == '|' || c == '&' || c == '(' || c == ')':
			flush()
			tok := string(c)
			if (c == '&' || c == '|') && i+1 < len(command) && command[i+1] == c {
				tok += string(c)
				i++
			}
			words = append(words, tok)
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	flush()
	return words
}
