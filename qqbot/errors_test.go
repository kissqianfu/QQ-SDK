package qqbot

import "testing"

func TestAPIErrorMethods(t *testing.T) {
	tests := []struct {
		name string
		err  *APIError
		want func(*APIError) bool
		ok   bool
	}{
		{name: "duplicate", err: &APIError{Code: 40054005}, want: (*APIError).IsDuplicate, ok: true},
		{name: "not duplicate", err: &APIError{Code: 40054007}, want: (*APIError).IsDuplicate, ok: false},
		{name: "expired passive", err: &APIError{Code: 40034128}, want: (*APIError).IsExpired, ok: true},
		{name: "expired old", err: &APIError{Code: 304103}, want: (*APIError).IsExpired, ok: true},
		{name: "expired message", err: &APIError{Code: 40034005}, want: (*APIError).IsExpired, ok: true},
		{name: "not expired", err: &APIError{Code: 40034100}, want: (*APIError).IsExpired, ok: false},
		{name: "auth", err: &APIError{Code: 304004}, want: (*APIError).IsAuth, ok: true},
		{name: "auth perm", err: &APIError{Code: 40034127}, want: (*APIError).IsAuth, ok: true},
		{name: "not auth", err: &APIError{Code: 500}, want: (*APIError).IsAuth, ok: false},
		{name: "rate limited code", err: &APIError{Code: 40034100}, want: (*APIError).IsRateLimited, ok: true},
		{name: "rate limited http", err: &APIError{HTTP: 429}, want: (*APIError).IsRateLimited, ok: true},
		{name: "not rate limited", err: &APIError{Code: 40054005}, want: (*APIError).IsRateLimited, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.want(tt.err); got != tt.ok {
				t.Fatalf("%s = %v, want %v", tt.name, got, tt.ok)
			}
		})
	}
}

func TestAPIErrorString(t *testing.T) {
	err := &APIError{Code: 40054005, Message: "dup", HTTP: 400, TraceID: "trace-1"}
	if got := err.Error(); got == "" {
		t.Fatal("Error() = empty")
	}
}
