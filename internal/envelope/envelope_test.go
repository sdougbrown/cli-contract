package envelope

import (
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"testing"
)

// TestError pins the wire shape consumers key on: the members and their
// values. Every reference adapter builds its failure envelope here, so a
// change to this shape is a wire change, not an internal refactor.
func TestError(t *testing.T) {
	got := Error("check", "usage", errors.New("bad schema file"))
	want := map[string]any{
		"command": "check",
		"status":  "error",
		"error":   map[string]any{"code": "usage", "message": "bad schema file"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("envelope = %#v, want %#v", got, want)
	}

	// The map keys are the JSON field names; pin them so a rename cannot
	// ride along with a matching edit to the expected map above.
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	keys := slices.Sorted(maps.Keys(decoded))
	if want := []string{"command", "error", "status"}; !slices.Equal(keys, want) {
		t.Fatalf("envelope keys = %v, want %v", keys, want)
	}
	errObj, ok := decoded["error"].(map[string]any)
	if !ok {
		t.Fatalf("error member = %T, want a JSON object", decoded["error"])
	}
	keys = slices.Sorted(maps.Keys(errObj))
	if want := []string{"code", "message"}; !slices.Equal(keys, want) {
		t.Fatalf("error member keys = %v, want %v", keys, want)
	}
}
