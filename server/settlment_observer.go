package server

import (
	"time"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type settlementObserver struct {
	histogram *prometheus.HistogramVec
	logger    hclog.Logger
}

func newSettlementObserver(logger hclog.Logger) *settlementObserver {
	hist := promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "ucl",
			Subsystem: "txpool",
			Name:      "settlement_seconds",
			Help:      "Seconds from tx first seen in txpool to block inclusion.",
			Buckets:   []float64{0.5, 1, 2, 4, 8, 15, 30, 60, 120, 300},
		},
		[]string{"origin"},
	)

	return &settlementObserver{
		histogram: hist,
		logger:    logger.Named("settlement-observer"),
	}
}

func (o *settlementObserver) OnTxsIncluded(txs []*types.Transaction) {
	now := time.Now().UTC()

	for _, tx := range txs {
		if tx.Type == types.StateTx || tx.TxPoolTime == 0 {
			continue
		}

		seenAt := time.Unix(tx.TxPoolTime, 0).UTC()
		if seenAt.After(now) {
			o.logger.Warn("tx seen after inclusion, skipping",
				"hash", tx.Hash,
				"seenAt", seenAt,
				"now", now,
			)

			continue
		}

		delta := now.Sub(seenAt).Seconds()
		o.histogram.WithLabelValues("unknown").Observe(delta)

		o.logger.Debug("observed settlement time",
			"hash", tx.Hash,
			"delta_seconds", delta,
		)
	}
}
