// Package optional holds Value, a JSON member that tells absent, null and a
// value apart.
//
// The schema uses it where the protocol gives the three different meanings,
// as in an update that omits a member to keep the stored value, sends null to
// clear it and sends a value to replace it:
//
//	update := schema.SessionUpdateSessionInfoUpdate{
//		Title:     optional.Of("Refactor"), // replace
//		UpdatedAt: optional.Null[string](),  // clear
//		// Meta left zero: unchanged
//	}
//
// A member declared with omitzero is left out while it is absent.
package optional

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
)

// Value is an absent, null or present T. The zero value is absent.
type Value[T any] struct {
	value T
	state state
}

type state uint8

const (
	absent state = iota
	null
	present
)

// Of returns a present v.
func Of[T any](v T) Value[T] { return Value[T]{value: v, state: present} }

// Null returns a null Value.
func Null[T any]() Value[T] { return Value[T]{state: null} }

// Get returns the value, and whether there is one: it is the zero T and
// false when v is absent or null.
func (v Value[T]) Get() (T, bool) { return v.value, v.state == present }

// Or returns the value, or fallback when v is absent or null.
func (v Value[T]) Or(fallback T) T {
	if v.state == present {
		return v.value
	}
	return fallback
}

// IsNull reports whether v is null.
func (v Value[T]) IsNull() bool { return v.state == null }

// IsZero reports whether v is absent, so omitzero omits it.
func (v Value[T]) IsZero() bool { return v.state == absent }

// MarshalJSONTo implements [json.MarshalerTo]. An absent value, which
// omitzero normally leaves out, encodes as null. The pointer receiver lets
// the encoder pass the held value on without copying it.
func (v *Value[T]) MarshalJSONTo(enc *jsontext.Encoder) error {
	if v.state != present {
		return enc.WriteToken(jsontext.Null)
	}
	return json.MarshalEncode(enc, &v.value)
}

// UnmarshalJSONFrom implements [json.UnmarshalerFrom]. The decoder calls it
// only for a member that is there, so a member left out stays absent.
func (v *Value[T]) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	if dec.PeekKind() == 'n' {
		if _, err := dec.ReadToken(); err != nil {
			return err
		}
		*v = Null[T]()
		return nil
	}
	var zero T
	v.value = zero
	if err := json.UnmarshalDecode(dec, &v.value); err != nil {
		return err
	}
	v.state = present
	return nil
}
