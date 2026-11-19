package cp

import (
	"context"
	"errors"
	"testing"
)

func TestIsRefusal(t *testing.T) {
	if !IsRefusal(&Error{Status: 409, Code: "bootstrap_subject"}) {
		t.Error("409 bootstrap_subject must classify as a refusal")
	}
	if IsRefusal(context.Canceled) {
		t.Error("a context error must not classify as a refusal")
	}
	if !IsRefusal(errors.Join(errors.New("wrapped"), &Error{Status: 409})) {
		t.Error("a wrapped refusal must still classify as a refusal")
	}
}
