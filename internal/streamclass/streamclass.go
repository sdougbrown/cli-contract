// Package streamclass names the stream shapes a conformance case can require
// and judges one stream against them. The black-box checker and the reference
// fixture runner both use this vocabulary, so a checker case and a fixture
// case state the same thing in the same words.
package streamclass

import (
	"encoding/json"
	"strings"
)

// Class names a stream shape a case can require.
const (
	Empty     = "empty"
	NonEmpty  = "non-empty"
	PlainText = "plain-text"
	JSONValue = "json-value"
)

// Reason explains why a stream did not satisfy its class.
type Reason string

const (
	// ReasonOK means the stream satisfied its class.
	ReasonOK Reason = ""
	// ReasonUnknownClass means the class names no shape.
	ReasonUnknownClass Reason = "unknown class"

	ReasonNotEmpty      Reason = "is not empty"
	ReasonEmpty         Reason = "is empty"
	ReasonEmptyWantText Reason = "is empty, want plain text"
	ReasonJSONWantText  Reason = "is a JSON value, want plain text"
	ReasonNotJSON       Reason = "is not one JSON value"
)

// String renders the reason as the phrase a caller appends to a stream name.
func (r Reason) String() string { return string(r) }

// OK reports whether the stream satisfied its class.
func (r Reason) OK() bool { return r == ReasonOK }

// Judge tests one stream against the class it must satisfy. The trim rule and
// JSON detection are the whole definition of the four classes: a stream is
// empty when it holds only whitespace, and JSON when it decodes as exactly one
// JSON value.
func Judge(class, got string) Reason {
	switch class {
	case Empty:
		if strings.TrimSpace(got) != "" {
			return ReasonNotEmpty
		}
	case NonEmpty:
		if strings.TrimSpace(got) == "" {
			return ReasonEmpty
		}
	case PlainText:
		if strings.TrimSpace(got) == "" {
			return ReasonEmptyWantText
		}
		if _, ok := DecodeOneJSONValue(got); ok {
			return ReasonJSONWantText
		}
	case JSONValue:
		if _, ok := DecodeOneJSONValue(got); !ok {
			return ReasonNotJSON
		}
	default:
		return ReasonUnknownClass
	}
	return ReasonOK
}

// DecodeOneJSONValue decodes exactly one JSON value from one stream.
func DecodeOneJSONValue(s string) (any, bool) {
	if strings.TrimSpace(s) == "" {
		return nil, false
	}
	var value any
	if err := json.Unmarshal([]byte(s), &value); err != nil {
		return nil, false
	}
	return value, true
}
