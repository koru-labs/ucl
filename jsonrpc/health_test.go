package jsonrpc

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// /health answers 200 while the check passes (or there is none) and 503 with the reason once it
// fails; it takes GET and HEAD only.
func TestHealthAnswersTheCheck(t *testing.T) {
	var health error

	ask := func(check HealthCheck, method string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		healthHandler(check)(w, httptest.NewRequest(method, "/health", nil))

		return w
	}
	check := func() error { return health }

	w := ask(check, http.MethodGet)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "ok\n", w.Body.String())
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))

	require.Equal(t, http.StatusOK, ask(nil, http.MethodGet).Code, "no check: healthy")
	require.Equal(t, http.StatusOK, ask(check, http.MethodHead).Code)

	health = errors.New("ucl has halted for the migration to pitau: no block after the cutover block 5")
	w = ask(check, http.MethodGet)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "cutover block 5")
	require.Equal(t, http.StatusServiceUnavailable, ask(check, http.MethodHead).Code)

	require.Equal(t, http.StatusMethodNotAllowed, ask(check, http.MethodPost).Code)
}

// The JSON-RPC server serves /health beside JSON-RPC, with the configured check.
func TestHealthIsServedByTheJSONRPCServer(t *testing.T) {
	jsonRPC, err := newTestJSONRPC(t)
	require.NoError(t, err)

	jsonRPC.config.Health = func() error { return errors.New("halted") }
	mux := jsonRPC.mux()

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}
