package k8s

import (
	"fmt"
	"path/filepath"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// NewConfig builds a rest.Config. It tries in-cluster config first, then
// falls back to the kubeconfig at the given path (or the default location).
func NewConfig(kubeconfigPath string) (*rest.Config, error) {
	cfg, err := rest.InClusterConfig()
	if err == nil {
		return cfg, nil
	}

	if kubeconfigPath == "" {
		home := homedir.HomeDir()
		if home == "" {
			return nil, fmt.Errorf("kubeconfig not specified and home directory not found")
		}
		kubeconfigPath = filepath.Join(home, ".kube", "config")
	}

	cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("building kubeconfig from %s: %w", kubeconfigPath, err)
	}
	return cfg, nil
}

// NewClientset creates a Kubernetes clientset from the given rest.Config.
func NewClientset(cfg *rest.Config) (kubernetes.Interface, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating clientset: %w", err)
	}
	return cs, nil
}

// NewInformerFactory creates a shared informer factory from the given clientset.
func NewInformerFactory(cs kubernetes.Interface) informers.SharedInformerFactory {
	return informers.NewSharedInformerFactory(cs, 0)
}
