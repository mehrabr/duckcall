package main

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sync"
)

// metrics is a small hand-rolled registry exposing Prometheus text format.
// The proxy needs three counters and two gauges; that does not justify a
// dependency, and duckcall staying dependency-free is a feature.
type metrics struct {
	mu       sync.Mutex
	counters map[string]map[string]float64 // name -> label string -> value
	gauges   map[string]func() float64
}

func newMetrics() *metrics {
	return &metrics{
		counters: map[string]map[string]float64{},
		gauges:   map[string]func() float64{},
	}
}

func (m *metrics) inc(name, labels string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.counters[name]
	if c == nil {
		c = map[string]float64{}
		m.counters[name] = c
	}
	c[labels]++
}

// gauge registers a callback sampled at scrape time.
func (m *metrics) gauge(name string, fn func() float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gauges[name] = fn
}

func (m *metrics) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")

	for _, name := range slices.Sorted(maps.Keys(m.counters)) {
		fmt.Fprintf(w, "# TYPE %s counter\n", name)
		for _, labels := range slices.Sorted(maps.Keys(m.counters[name])) {
			fmt.Fprintf(w, "%s%s %g\n", name, labels, m.counters[name][labels])
		}
	}
	for _, name := range slices.Sorted(maps.Keys(m.gauges)) {
		fmt.Fprintf(w, "# TYPE %s gauge\n", name)
		fmt.Fprintf(w, "%s %g\n", name, m.gauges[name]())
	}
}
