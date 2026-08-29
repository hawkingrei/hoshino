package main

import (
	"github.com/hawkingrei/hoshino/diskutil"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

type DefaultFieldsFormatter struct {
	WrappedFormatter logrus.Formatter
	DefaultFields    logrus.Fields
}

func NewDefaultFieldsFormatter(wrappedFormatter logrus.Formatter, defaultFields logrus.Fields) *DefaultFieldsFormatter {
	if wrappedFormatter == nil {
		wrappedFormatter = &logrus.JSONFormatter{}
	}
	return &DefaultFieldsFormatter{
		WrappedFormatter: wrappedFormatter,
		DefaultFields:    defaultFields,
	}
}

func (d *DefaultFieldsFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	data := make(logrus.Fields, len(entry.Data)+len(d.DefaultFields))
	for key, value := range d.DefaultFields {
		data[key] = value
	}
	for key, value := range entry.Data {
		data[key] = value
	}
	return d.WrappedFormatter.Format(&logrus.Entry{
		Logger:  entry.Logger,
		Data:    data,
		Time:    entry.Time,
		Level:   entry.Level,
		Message: entry.Message,
	})
}

type metrics struct {
	diskFreeBytes   prometheus.Gauge
	diskUsedBytes   prometheus.Gauge
	cacheBytes      prometheus.Gauge
	ready           prometheus.Gauge
	projectedBytes  prometheus.Gauge
	accesses        *prometheus.CounterVec
	closes          *prometheus.CounterVec
	deletedBytes    *prometheus.CounterVec
	deletedFiles    *prometheus.CounterVec
	skippedActive   prometheus.Counter
	frozen          *prometheus.CounterVec
	reconciliations prometheus.Counter
}

func newMetrics(registerer prometheus.Registerer) *metrics {
	result := &metrics{
		diskFreeBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hoshino_nodefs_free_bytes",
			Help: "Free bytes on the filesystem containing the Bazel disk cache.",
		}),
		diskUsedBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hoshino_nodefs_used_bytes",
			Help: "Used bytes on the filesystem containing the Bazel disk cache.",
		}),
		cacheBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hoshino_cache_bytes",
			Help: "Bytes in validated Bazel action-cache and CAS entries.",
		}),
		ready: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hoshino_ready",
			Help: "Whether Hoshino has complete watcher coverage.",
		}),
		projectedBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hoshino_projected_eviction_bytes",
			Help: "Bytes selected by the latest shadow eviction cycle.",
		}),
		accesses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hoshino_cache_accesses_total",
			Help: "Observed opens of completed Bazel disk-cache entries.",
		}, []string{"store"}),
		closes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hoshino_cache_closes_total",
			Help: "Observed closes of completed Bazel disk-cache entries.",
		}, []string{"store"}),
		deletedBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hoshino_evicted_bytes_total",
			Help: "Bytes deleted from validated Bazel disk-cache entries.",
		}, []string{"store"}),
		deletedFiles: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hoshino_evicted_files_total",
			Help: "Validated Bazel disk-cache entries deleted.",
		}, []string{"store"}),
		skippedActive: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "hoshino_eviction_skipped_active_total",
			Help: "Eviction cycles skipped because a Bazel invocation held the shared lease.",
		}),
		frozen: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hoshino_eviction_frozen_total",
			Help: "Eviction cycles frozen by a safety gate.",
		}, []string{"reason"}),
		reconciliations: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "hoshino_watcher_reconciliations_total",
			Help: "Successful full watcher and cache-tree reconciliations.",
		}),
	}
	registerer.MustRegister(
		result.diskFreeBytes,
		result.diskUsedBytes,
		result.cacheBytes,
		result.ready,
		result.projectedBytes,
		result.accesses,
		result.closes,
		result.deletedBytes,
		result.deletedFiles,
		result.skippedActive,
		result.frozen,
		result.reconciliations,
	)
	return result
}

func (m *metrics) SetDisk(bytesFree, bytesUsed uint64) {
	m.diskFreeBytes.Set(float64(bytesFree))
	m.diskUsedBytes.Set(float64(bytesUsed))
}

func (m *metrics) SetCacheBytes(bytes int64) {
	m.cacheBytes.Set(float64(bytes))
}

func (m *metrics) SetReady(ready bool) {
	if ready {
		m.ready.Set(1)
		return
	}
	m.ready.Set(0)
}

func (m *metrics) RecordAccess(kind diskutil.EntryKind) {
	m.accesses.WithLabelValues(string(kind)).Inc()
}

func (m *metrics) RecordClose(kind diskutil.EntryKind) {
	m.closes.WithLabelValues(string(kind)).Inc()
}

func (m *metrics) RecordProjected(bytes int64) {
	m.projectedBytes.Set(float64(bytes))
}

func (m *metrics) RecordDeleted(kind diskutil.EntryKind, bytes int64) {
	m.deletedBytes.WithLabelValues(string(kind)).Add(float64(bytes))
	m.deletedFiles.WithLabelValues(string(kind)).Inc()
}

func (m *metrics) RecordSkippedActive() {
	m.skippedActive.Inc()
}

func (m *metrics) RecordFrozen(reason string) {
	m.frozen.WithLabelValues(reason).Inc()
}

func (m *metrics) RecordReconciliation() {
	m.reconciliations.Inc()
}
