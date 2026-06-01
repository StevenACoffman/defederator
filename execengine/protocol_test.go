package execengine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestExecute_ProtocolEdgeCases verifies Layer 1: the executor handles all legal
// GraphQL HTTP response shapes without panicking or silently discarding information.
func TestExecute_ProtocolEdgeCases(t *testing.T) {
	simplePlan := func(url string) *Plan {
		return &Plan{
			Fetches: []Fetch{{URL: url, Query: `{ q }`}},
		}
	}

	cases := map[string]struct {
		handler      func(t *testing.T) http.HandlerFunc
		wantTransErr bool   // transport/decode error (err != nil)
		wantErrCount int    // GraphQL-level errors
		wantErrMsg   string // substring in err.Error() when wantTransErr
	}{
		// GraphQL spec §7.1: servers MAY use HTTP status codes for non-data responses,
		// but a valid GraphQL body must still be parsed regardless of status.
		"http_200_with_errors": {
			handler: func(t *testing.T) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(`{"data":null,"errors":[{"message":"not found"}]}`))
				}
			},
			wantTransErr: false,
			wantErrCount: 1,
		},
		// Body is not JSON at all — must return a transport/decode error.
		"malformed_json": {
			handler: func(t *testing.T) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					w.Write([]byte(`not-valid-json`))
				}
			},
			wantTransErr: true,
		},
		// data:null with errors — errors must be propagated, no panic.
		"data_null_with_errors": {
			handler: func(t *testing.T) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					w.Write([]byte(`{"data":null,"errors":[{"message":"upstream failure"}]}`))
				}
			},
			wantTransErr: false,
			wantErrCount: 1,
		},
		// data:null with no errors — must return empty data without error.
		"data_null_no_errors": {
			handler: func(t *testing.T) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					w.Write([]byte(`{"data":null}`))
				}
			},
			wantTransErr: false,
			wantErrCount: 0,
		},
		// Empty data object — not an error, empty merged result.
		"data_empty_object": {
			handler: func(t *testing.T) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					w.Write([]byte(`{"data":{}}`))
				}
			},
			wantTransErr: false,
			wantErrCount: 0,
		},
		// Pre-cancelled context — Execute must not make a network call and must
		// return a non-nil error containing "context". Uses a normal server that
		// would succeed, so the error is unambiguously from context cancellation.
		"context_precancelled": {
			handler: func(t *testing.T) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					w.Write([]byte(`{"data":{}}`))
				}
			},
			wantTransErr: true,
			wantErrMsg:   "context",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler(t))
			defer srv.Close()

			ctx := context.Background()
			if name == "context_precancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel() // cancel before Execute is called
			}

			_, errs, err := execute(ctx, simplePlan(srv.URL), nil, nil, false)

			if tc.wantTransErr {
				if err == nil {
					t.Errorf("expected a transport/decode error, got nil")
				} else if tc.wantErrMsg != "" && !strings.Contains(err.Error(), tc.wantErrMsg) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErrMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected transport error: %v", err)
			}
			if len(errs) != tc.wantErrCount {
				t.Errorf(
					"GraphQL error count: got %d, want %d (errs=%v)",
					len(errs),
					tc.wantErrCount,
					errs,
				)
			}
		})
	}
}
