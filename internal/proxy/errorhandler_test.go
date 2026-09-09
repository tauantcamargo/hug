package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientGone(t *testing.T) {
	plain := httptest.NewRequest(http.MethodGet, "/backend-api/codex/models", nil)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	hungUp := plain.Clone(cancelled)

	cases := []struct {
		name string
		err  error
		req  *http.Request
		want bool
	}{
		{"client cancelled the request", context.Canceled, plain, true},
		{"cancellation wrapped by the transport", fmt.Errorf("proxy: %w", context.Canceled), plain, true},
		{"request context already cancelled", errors.New("some transport error"), hungUp, true},
		{"real upstream failure", errors.New("connection refused"), plain, false},
		{"upstream timed out", context.DeadlineExceeded, plain, false},
	}
	for _, c := range cases {
		if got := clientGone(c.err, c.req); got != c.want {
			t.Errorf("%s: clientGone = %v, want %v", c.name, got, c.want)
		}
	}
}
