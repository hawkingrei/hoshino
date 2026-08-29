package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/hawkingrei/hoshino/eviction"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
)

const gibibyte = int64(1 << 30)

type options struct {
	cacheDir                    string
	activityLock                string
	checkpointFile              string
	host                        string
	metricsPort                 int
	pprofHost                   string
	pprofPort                   int
	shadow                      bool
	diskCheckInterval           time.Duration
	checkpointInterval          time.Duration
	minimumResidence            time.Duration
	warmupDuration              time.Duration
	topKDecayInterval           time.Duration
	minPercentBlocksFree        float64
	evictUntilPercentBlocksFree float64
	maxCacheGiB                 uint64
	targetCacheGiB              uint64
	minActionCacheGiB           uint64
	cleanupBatchSize            int
	topKCapacity                uint
	topKWidth                   uint
	topKDepth                   uint
	topKDecay                   float64
	topKMinCount                uint
}

func parseOptions(args []string) (options, error) {
	var opts options
	flags := flag.NewFlagSet("hoshino", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.cacheDir, "cache-dir", "", "Bazel native disk-cache root")
	flags.StringVar(&opts.activityLock, "activity-lock", "", "shared Bazel activity lock file")
	flags.StringVar(&opts.checkpointFile, "checkpoint-file", "", "hot-set checkpoint file")
	flags.StringVar(&opts.host, "host", "0.0.0.0", "health and metrics listen address")
	flags.IntVar(&opts.metricsPort, "metrics-port", 9092, "health and metrics listen port")
	flags.StringVar(&opts.pprofHost, "pprof-host", "127.0.0.1", "pprof listen address")
	flags.IntVar(&opts.pprofPort, "pprof-port", 0, "pprof listen port; zero disables pprof")
	flags.BoolVar(&opts.shadow, "shadow", true, "propose victims without deleting cache entries")
	flags.DurationVar(&opts.diskCheckInterval, "disk-check-interval", time.Minute, "interval between cache pressure checks")
	flags.DurationVar(&opts.checkpointInterval, "checkpoint-interval", 5*time.Minute, "interval between hot-set checkpoints")
	flags.DurationVar(&opts.minimumResidence, "minimum-residence", time.Hour, "minimum entry age before eviction")
	flags.DurationVar(&opts.warmupDuration, "warmup-duration", 30*time.Minute, "deletion freeze after startup without a checkpoint or watcher loss")
	flags.DurationVar(&opts.topKDecayInterval, "top-k-decay-interval", time.Hour, "interval between HeavyKeeper decay passes")
	flags.Float64Var(&opts.minPercentBlocksFree, "min-percent-blocks-free", 30, "node free-space percentage that starts eviction")
	flags.Float64Var(&opts.evictUntilPercentBlocksFree, "evict-until-percent-blocks-free", 35, "node free-space percentage that stops eviction")
	flags.Uint64Var(&opts.maxCacheGiB, "max-cache-gib", 400, "cache size in GiB that starts eviction")
	flags.Uint64Var(&opts.targetCacheGiB, "target-cache-gib", 320, "cache size in GiB that stops eviction")
	flags.Uint64Var(&opts.minActionCacheGiB, "min-action-cache-gib", 4, "minimum protected action-cache budget in GiB")
	flags.IntVar(&opts.cleanupBatchSize, "cleanup-batch-size", 100, "entries deleted between disk usage refreshes")
	flags.UintVar(&opts.topKCapacity, "top-k-capacity", 100_000, "maximum number of hot keys retained")
	flags.UintVar(&opts.topKWidth, "top-k-width", 1<<17, "HeavyKeeper sketch width")
	flags.UintVar(&opts.topKDepth, "top-k-depth", 4, "HeavyKeeper sketch depth")
	flags.Float64Var(&opts.topKDecay, "top-k-decay", 0.9, "HeavyKeeper decay probability")
	flags.UintVar(&opts.topKMinCount, "top-k-min-count", 1, "minimum count admitted to the hot set")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	if opts.cacheDir == "" {
		return options{}, errors.New("--cache-dir must be set")
	}
	if opts.activityLock == "" {
		opts.activityLock = filepath.Join(filepath.Dir(opts.cacheDir), "bazel-cache.activity.lock")
	}
	if opts.checkpointFile == "" {
		opts.checkpointFile = filepath.Join(filepath.Dir(opts.cacheDir), "hoshino-state", "hot-set.json")
	}
	if opts.metricsPort <= 0 || opts.metricsPort > 65535 || opts.pprofPort < 0 || opts.pprofPort > 65535 {
		return options{}, errors.New("listen ports must be in range 1-65535; pprof may also be zero")
	}
	return opts, nil
}

func (o options) evictionConfig(observer eviction.Observer) (eviction.Config, error) {
	maxCacheBytes, err := gibToBytes(o.maxCacheGiB)
	if err != nil {
		return eviction.Config{}, err
	}
	targetCacheBytes, err := gibToBytes(o.targetCacheGiB)
	if err != nil {
		return eviction.Config{}, err
	}
	minActionCacheBytes, err := gibToBytes(o.minActionCacheGiB)
	if err != nil {
		return eviction.Config{}, err
	}
	if o.topKCapacity > math.MaxUint32 || o.topKWidth > math.MaxUint32 || o.topKDepth > math.MaxUint32 || o.topKMinCount > math.MaxUint32 {
		return eviction.Config{}, errors.New("top-k values must fit in uint32")
	}
	return eviction.Config{
		CacheDir:       o.cacheDir,
		ActivityLock:   o.activityLock,
		CheckpointFile: o.checkpointFile,
		Observer:       observer,
		Policy: eviction.EvictionPolicy{
			DiskCheckInterval:           o.diskCheckInterval,
			CheckpointInterval:          o.checkpointInterval,
			TopKDecayInterval:           o.topKDecayInterval,
			MinimumResidence:            o.minimumResidence,
			WarmupDuration:              o.warmupDuration,
			MinPercentBlocksFree:        o.minPercentBlocksFree,
			EvictUntilPercentBlocksFree: o.evictUntilPercentBlocksFree,
			MaxCacheBytes:               maxCacheBytes,
			TargetCacheBytes:            targetCacheBytes,
			MinActionCacheBytes:         minActionCacheBytes,
			CleanupBatchSize:            o.cleanupBatchSize,
			TopKCapacity:                uint32(o.topKCapacity),
			TopKWidth:                   uint32(o.topKWidth),
			TopKDepth:                   uint32(o.topKDepth),
			TopKDecay:                   o.topKDecay,
			TopKMinCount:                uint32(o.topKMinCount),
			Shadow:                      o.shadow,
		},
	}, nil
}

func gibToBytes(value uint64) (int64, error) {
	if value > math.MaxInt64/uint64(gibibyte) {
		return 0, fmt.Errorf("GiB value is too large: %d", value)
	}
	return int64(value) * gibibyte, nil
}

func run(ctx context.Context, opts options) error {
	metrics := newMetrics(prometheus.DefaultRegisterer)
	config, err := opts.evictionConfig(metrics)
	if err != nil {
		return err
	}
	manager, err := eviction.New(config)
	if err != nil {
		return err
	}

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsMux.Handle("/prometheus", promhttp.Handler())
	metricsMux.HandleFunc("/healthz", func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("ok\n"))
	})
	metricsMux.HandleFunc("/readyz", func(response http.ResponseWriter, _ *http.Request) {
		if !manager.Ready() {
			http.Error(response, "not ready", http.StatusServiceUnavailable)
			return
		}
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("ready\n"))
	})
	metricsServer := &http.Server{
		Addr:              opts.host + ":" + strconv.Itoa(opts.metricsPort),
		Handler:           metricsMux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	var pprofServer *http.Server
	if opts.pprofPort != 0 {
		pprofMux := http.NewServeMux()
		pprofMux.HandleFunc("/debug/pprof/", pprof.Index)
		pprofMux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		pprofMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		pprofMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		pprofMux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		pprofServer = &http.Server{
			Addr:              opts.pprofHost + ":" + strconv.Itoa(opts.pprofPort),
			Handler:           pprofMux,
			ReadHeaderTimeout: 5 * time.Second,
		}
	}

	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	managerErrors := make(chan error, 1)
	serverErrors := make(chan error, 2)
	go func() { managerErrors <- manager.Run(runContext) }()
	go func() {
		if err := metricsServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()
	if pprofServer != nil {
		go func() {
			if err := pprofServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				serverErrors <- err
			}
		}()
	}

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-managerErrors:
	case runErr = <-serverErrors:
	}
	cancel()
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	runErr = errors.Join(runErr, metricsServer.Shutdown(shutdownContext))
	if pprofServer != nil {
		runErr = errors.Join(runErr, pprofServer.Shutdown(shutdownContext))
	}
	return runErr
}

func main() {
	logrus.SetFormatter(NewDefaultFieldsFormatter(nil, logrus.Fields{"component": "hoshino"}))
	logrus.SetOutput(os.Stdout)
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		logrus.WithError(err).Fatal("Invalid configuration")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, opts); err != nil {
		logrus.WithError(err).Fatal("Hoshino stopped")
	}
}
