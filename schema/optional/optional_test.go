package optional_test

import (
	"encoding/json/v2"
	"testing"

	"github.com/ironpark/acp-go/schema/optional"
)

type update struct {
	Title optional.Value[string]   `json:"title,omitzero"`
	Tags  optional.Value[[]string] `json:"tags,omitzero"`
}

func TestRoundTripKeepsAbsentNullAndValue(t *testing.T) {
	for _, want := range []string{`{}`, `{"title":null}`, `{"title":"x","tags":[]}`, `{"tags":null}`} {
		var u update
		if err := json.Unmarshal([]byte(want), &u); err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(u) // by value: the encoder must still find the pointer method
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("%s: round trip gave %s", want, got)
		}
	}
}

func TestStates(t *testing.T) {
	var u update
	if err := json.Unmarshal([]byte(`{"title":null}`), &u); err != nil {
		t.Fatal(err)
	}
	if !u.Title.IsNull() || u.Title.IsZero() || u.Tags.IsNull() || !u.Tags.IsZero() {
		t.Fatalf("null title, absent tags: got %+v", u)
	}
	if v, ok := u.Title.Get(); ok || v != "" || u.Title.Or("kept") != "kept" {
		t.Fatal("null has no value")
	}
	if v, ok := optional.Of("x").Get(); !ok || v != "x" {
		t.Fatal("Of lost its value")
	}
	if err := json.Unmarshal([]byte(`{"title":1}`), &u); err == nil {
		t.Fatal("decoded a number into a string")
	}
}
