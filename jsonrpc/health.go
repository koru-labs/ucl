package jsonrpc

import (
	"fmt"
	"net/http"
)

// HealthCheck is nil while the node should get traffic, and the reason otherwise.
type HealthCheck func() error

// healthHandler answers load balancers' health checks, GET or HEAD /health: 200 "ok" while check
// is nil (or there is no check), 503 with the reason otherwise. A ucl node answers 503 once it has
// halted for the migration to pitau (Blockchain.MigrationHealth), so a load balancer stops sending
// it requests and sends them to pitau's nodes, which answer 200 from the cutover on.
func healthHandler(check HealthCheck) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")

		if check != nil {
			if err := check(); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprintf(w, "unhealthy: %v\n", err)

				return
			}
		}

		fmt.Fprint(w, "ok\n")
	}
}
