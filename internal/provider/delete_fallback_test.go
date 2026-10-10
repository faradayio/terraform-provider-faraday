package provider

// `neverBuilt` is the one predicate in this provider whose failure mode is a
// hard delete, so its narrowness is worth pinning down. Everything it does not
// match keeps archiving, which is the safe default.

import (
	"fmt"
	"net/http"
	"testing"
)

func TestNeverBuilt(t *testing.T) {
	// The real 400 body, verified against local dev.
	notBuilt := `{"id":"d3709138-f444-45b8-a795-4bc6190b2e28","error":"VALIDATION_FAILED",` +
		`"note":"Cannot archive resource 5961b28e-4820-4656-8115-1a4ed3b91a3f because it has not built yet."}`

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "the archive precondition failure",
			err:  &apiError{status: http.StatusBadRequest, body: notBuilt},
			want: true,
		},
		{
			// The reason matching the status code alone is not enough: the API
			// reuses VALIDATION_FAILED, so a 400 that means something else
			// entirely must keep archiving rather than fall through to DELETE.
			name: "a different validation failure at the same status",
			err: &apiError{
				status: http.StatusBadRequest,
				body:   `{"error":"VALIDATION_FAILED","note":"name is required."}`,
			},
			want: false,
		},
		{
			name: "cascade refusal, which must never hard delete",
			err: &apiError{
				status: http.StatusBadRequest,
				body:   `{"error":"VALIDATION_FAILED","note":"downstream resources would be archived."}`,
			},
			want: false,
		},
		{
			// Same words, wrong status: a 500 mentioning this is a server fault,
			// not the documented precondition.
			name: "right message at the wrong status",
			err:  &apiError{status: http.StatusInternalServerError, body: notBuilt},
			want: false,
		},
		{
			name: "not found",
			err:  errNotFound,
			want: false,
		},
		{
			name: "a transport error carrying no status at all",
			err:  fmt.Errorf("connection refused"),
			want: false,
		},
		{
			// Delete inspects whatever the client hands back, so the check has
			// to survive wrapping rather than depending on the concrete type.
			name: "wrapped",
			err:  fmt.Errorf("archiving cohort: %w", &apiError{status: http.StatusBadRequest, body: notBuilt}),
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := neverBuilt(tc.err); got != tc.want {
				t.Errorf("neverBuilt(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestNeverBuiltIgnoresNil(t *testing.T) {
	if neverBuilt(nil) {
		t.Error("neverBuilt(nil) = true, want false")
	}
}
