package server

import (
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

func (o *settlementObserver) OnTxsIncluded(settlmentMetrics []float64) {
	for _, metric := range settlmentMetrics {
		o.histogram.WithLabelValues("unknown").Observe(metric)
	}
}
