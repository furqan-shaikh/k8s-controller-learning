package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// 1. Register custom metrics
var (
	APIServerControllerSuccessTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "api_server_controller_success_total",
			Help: "The total number of times api server controller was in success state",
		},
		[]string{"version", "namespace"},
	)
)

// InitMetrics registers custom metrics with kubernetes global prometheus registry
func InitMetrics() {
	// Register custom metrics with the global prometheus registry
	ctrlmetrics.Registry.MustRegister(
		APIServerControllerSuccessTotal,
	)

	// Initialize counterVec with 0 values for expected label combinations
	APIServerControllerSuccessTotal.WithLabelValues("", "").Add(0)

}
