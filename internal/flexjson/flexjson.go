// Package flexjson holds JSON types for provider APIs that are loose about
// types: an ID documented as a string arrives as a number, a cost as either.
package flexjson

import "encoding/json"

// String decodes a JSON string or number into its text; null decodes to "".
type String string

func (f *String) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*f = ""
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = String(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = String(n.String())
	return nil
}
