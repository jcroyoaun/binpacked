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

	"github.com/zillow/binpacked/internal/api"
	k8sclient "github.com/zillow/binpacked/internal/k8s"
	"github.com/zillow/binpacked/internal/packing"
	"k8s.io/client-go/tools/cache"
)

//go:embed web/*
var webFS embed.FS

func main() {
	var addr string
	var kubeconfig string
	flag.StringVar(&addr, "addr", ":8080", "HTTP listen address")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig (uses in-cluster config if empty)")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	cs, err := k8sclient.NewClientset(kubeconfig)
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

	computer := packing.Computer{
		NodeLister: nodeInformer.Lister(),
		PodLister:  podInformer.Lister(),
	}
	nodes, err := computer.ComputeNodePacking()
	if err != nil {
		log.Fatalf("building initial cluster snapshot: %v", err)
	}
	logClusterSnapshot(nodes)

	mux := http.NewServeMux()

	handler := api.Handler{Computer: computer, Logger: log.Default()}
	handler.Register(mux)

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

	log.Printf("starting binpacked on %s", addr)
	log.Printf("dashboard available at http://localhost%s", addr)
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
