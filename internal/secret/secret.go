// Package secret holds a string that must never end up in a log, an error message
// or JSON output by accident, such as the Azure Monitor client secret.
package secret

import (
	"encoding/json"
	"fmt"
)

const redacted = "[redacted]"

// Secret is a string that hides itself when printed. Go's fmt package checks
// whether a value has a String or Format method and calls it, so giving Secret
// these methods means fmt.Println(s), "%v", "%+v" and "%#v" all print
// "[redacted]". The only way to get the real value is Reveal, which makes every
// use of the secret easy to find in the code.
type Secret string

// Reveal returns the real value. Only call it where the secret is actually used.
func (s Secret) Reveal() string { return string(s) }

// IsSet reports whether a value was given.
func (s Secret) IsSet() bool { return s != "" }

func (s Secret) String() string   { return redacted }
func (s Secret) GoString() string { return redacted }

// Format handles every fmt verb (%s, %v, %q, %x ...), so none of them leak it.
func (s Secret) Format(f fmt.State, _ rune) { fmt.Fprint(f, redacted) }

// MarshalJSON writes "[redacted]", so a config dumped as JSON doesn't leak it.
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }

// UnmarshalJSON reads the real value from a config file. (Marshal and Unmarshal
// are deliberately not symmetrical.)
func (s *Secret) UnmarshalJSON(b []byte) error {
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*s = Secret(v)
	return nil
}
