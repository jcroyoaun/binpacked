package main

import (
	"context"
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/client-go/tools/cache"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"

	"github.com/zillow/binpacked/internal/api"
	k8sclient "github.com/zillow/binpacked/internal/k8s"
	binmcp "github.com/zillow/binpacked/internal/mcp"
	"github.com/zillow/binpacked/internal/metrics"
	"github.com/zillow/binpacked/internal/packing"
)

//go:embed web/*
var webFS embed.FS

const version = "v1.1.0"

func main() {
	var addr string
	var kubeconfig string
	var mcpStdio bool
	var metricsInterval time.Duration
	var metricsWindow int
	flag.StringVar(&addr, "addr", ":8080", "HTTP listen address")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig (uses in-cluster config if empty)")
	flag.BoolVar(&mcpStdio, "mcp-stdio", false, "serve the MCP server on stdio instead of HTTP (for local agent use)")
	flag.DurationVar(&metricsInterval, "metrics-interval", 30*time.Second, "usage sampling interval for metrics-server (0 disables usage sampling)")
	flag.IntVar(&metricsWindow, "metrics-window", 120, "number of usage samples kept per container")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	// In stdio MCP mode, stdout carries the protocol: logs must stay on stderr
	// (the log package default), and nothing else may print to stdout.

	cfg, err := k8sclient.NewConfig(kubeconfig)
	if err != nil {
		log.Fatalf("building kubernetes config: %v", err)
	}
	cs, err := k8sclient.NewClientset(cfg)
	if err != nil {
		log.Fatalf("creating kubernetes client: %v", err)
	}

	factory := k8sclient.NewInformerFactory(cs)
	nodeInformer := factory.Core().V1().Nodes()
	podInformer := factory.Core().V1().Pods()
	nodeSharedInformer := nodeInformer.Informer()
	podSharedInformer := podInformer.Informer()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), nodeSharedInformer.HasSynced, podSharedInformer.HasSynced) {
		log.Fatal("failed to sync informer caches")
	}
	log.Println("informer caches synced")

	// Optional usage sampler (metrics-server). Degrades gracefully: when the
	// metrics API is unavailable, recommendations fall back to spec-only.
	var sampler *metrics.Sampler
	if metricsInterval > 0 {
		mc, err := metricsclient.NewForConfig(cfg)
		if err != nil {
			log.Printf("metrics client unavailable, usage sampling disabled: %v", err)
		} else {
			sampler = metrics.New(mc, metricsInterval, metricsWindow, log.Default())
			go sampler.Run(ctx)
		}
	}

	computer := packing.Computer{
		NodeLister: nodeInformer.Lister(),
		PodLister:  podInformer.Lister(),
	}
	if sampler != nil {
		computer.Usage = sampler
	}

	nodes, err := computer.ComputeNodePacking()
	if err != nil {
		log.Fatalf("building initial cluster snapshot: %v", err)
	}
	logClusterSnapshot(nodes)

	var usageInfo api.UsageInfo
	if sampler != nil {
		usageInfo = sampler
	}
	mcpServer := binmcp.NewServer(binmcp.Deps{
		Computer: computer,
		Usage:    usageInfo,
		Version:  version,
		Logger:   log.Default(),
	})

	if mcpStdio {
		log.Printf("serving MCP on stdio (binpacked %s)", version)
		if err := mcpServer.Run(ctx, &sdk.StdioTransport{}); err != nil && ctx.Err() == nil {
			log.Fatalf("mcp stdio server: %v", err)
		}
		return
	}

	mux := http.NewServeMux()

	handler := api.Handler{Computer: computer, Usage: usageInfo, Logger: log.Default()}
	handler.Register(mux)

	// MCP over streamable HTTP on the same port. Stateless: safe behind a
	// Service with multiple replicas; each request is self-contained.
	mcpHandler := sdk.NewStreamableHTTPHandler(
		func(r *http.Request) *sdk.Server { return mcpServer },
		&sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	// Register per-method so the pattern stays more specific than the
	// embedded frontend's "GET /" catch-all (Go 1.22 ServeMux conflict rule).
	mux.Handle("POST /mcp", mcpHandler)
	mux.Handle("GET /mcp", mcpHandler)
	mux.Handle("DELETE /mcp", mcpHandler)

	// Serve embedded frontend.
	webContent, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("embedding web assets: %v", err)
	}
	mux.Handle("GET /", http.FileServer(http.FS(webContent)))

	srv := http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown error: %v", err)
		}
	}()

	log.Printf("starting binpacked %s on %s", version, addr)
	log.Printf("dashboard at http://localhost%s, MCP endpoint at http://localhost%s/mcp", addr, addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}

func logClusterSnapshot(nodes []packing.NodePacking) {
	names := make([]string, 0, len(nodes))
	var totalPods int64

	for _, node := range nodes {
		names = append(names, node.Name)
		totalPods += node.Pods.Count
	}

	sort.Strings(names)
	log.Printf("initial cluster snapshot: nodes=%d pods=%d node_names=%v", len(nodes), totalPods, names)
}
