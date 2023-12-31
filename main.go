/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// greenhouse implements a bazel remote cache service [1]
// supporting arbitrarily many workspaces stored within the same
// top level directory.
//
// the first path segment in each {PUT,GET} request is mapped to an individual
// workspace cache, the remaining segments should follow [2].
//
// nursery assumes you are using SHA256
//
// [1] https://docs.bazel.build/versions/master/remote-caching.html
// [2] https://docs.bazel.build/versions/master/remote-caching.html#http-caching-protocol
package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/pprof"
	"os"
	"time"

	"github.com/hawkingrei/hoshino/diskutil"
	"github.com/hawkingrei/hoshino/eviction"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
)

var ListenDir = flag.String("listen-dir", "", "location to store cache entries on disk")
var dir = flag.String("dir", "", "location to store cache entries on disk")
var host = flag.String("host", "", "host address to listen on")
var cachePort = flag.Int("cache-port", 8080, "port to listen on for cache requests")
var metricsPort = flag.Int("metrics-port", 9092, "port to listen on for prometheus metrics scraping")
var pprofPort = flag.Int("pprof-port", 9091, "port to listen on for pprof")
var level = flag.Int("level", 3, "compression level")
var metricsUpdateInterval = flag.Duration("metrics-update-interval", time.Second*10,
	"interval between updating disk metrics")

// eviction knobs
var minPercentBlocksFree = flag.Float64("min-percent-blocks-free", 5,
	"minimum percent of blocks free on --dir's disk before evicting entries")
var evictUntilPercentBlocksFree = flag.Float64("evict-until-percent-blocks-free", 20,
	"continue evicting from the cache until at least this percent of blocks are free")
var diskCheckInterval = flag.Duration("disk-check-interval", time.Second*10,
	"interval between checking disk usage (and potentially evicting entries)")

// global metrics object, see prometheus.go
var promMetrics *prometheusMetrics

func init() {
	logrus.SetFormatter(
		NewDefaultFieldsFormatter(nil, logrus.Fields{"component": "greenhouse"}),
	)
	logrus.SetOutput(os.Stdout)
	promMetrics = initMetrics()
}

func main() {
	flag.Parse()
	if *dir == "" {
		logrus.Fatal("--dir must be set!")
	}
	if *ListenDir == "" {
		logrus.Fatal("--listen-dir must be set!")
	}
	notify := eviction.New(*dir, *ListenDir, *minPercentBlocksFree, *evictUntilPercentBlocksFree)
	go notify.Start()
	go notify.Background()

	go updateMetrics(*metricsUpdateInterval, *dir)

	// listen for prometheus scraping
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/prometheus", promhttp.Handler())
	metricsAddr := fmt.Sprintf("%s:%d", *host, *metricsPort)
	go func() {
		logrus.Infof("Metrics Listening on: %s", metricsAddr)
		logrus.WithField("mux", "metrics").WithError(
			http.ListenAndServe(metricsAddr, metricsMux),
		).Fatal("ListenAndServe returned.")
	}()

	// listen for pprofMux
	pprofMux := http.NewServeMux()
	pprofMux.HandleFunc("/debug/pprof/", pprof.Index)
	pprofMux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	pprofMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	pprofMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	pprofMux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	pprofAddr := fmt.Sprintf("%s:%d", *host, *pprofPort)
	logrus.Infof("pprof Listening on: %s", pprofAddr)
	logrus.WithField("mux", "pprof").WithError(
		http.ListenAndServe(pprofAddr, pprofMux),
	).Fatal("ListenAndServe returned.")
}

// file not found error, used below
var errNotFound = errors.New("entry not found")

// helper to update disk metrics
func updateMetrics(interval time.Duration, diskRoot string) {
	logger := logrus.WithField("sync-loop", "updateMetrics")
	ticker := time.NewTicker(interval)
	for ; true; <-ticker.C {
		_, bytesFree, bytesUsed, err := diskutil.GetDiskUsage(diskRoot)
		if err != nil {
			logger.WithError(err).Error("Failed to get disk metrics")
		} else {
			promMetrics.DiskFree.Set(float64(bytesFree) / 1e9)
			promMetrics.DiskUsed.Set(float64(bytesUsed) / 1e9)
			promMetrics.DiskTotal.Set(float64(bytesFree+bytesUsed) / 1e9)
		}
	}
}
