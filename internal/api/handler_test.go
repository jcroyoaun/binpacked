package api

import (
	"bytes"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zillow/binpacked/internal/packing"
)

type stubComputer struct {
	nodes    []packing.NodePacking
	pods     []packing.PodInfo
	nodesErr error
	podsErr  error
}

func (s stubComputer) ComputeNodePacking() ([]packing.NodePacking, error) {
	if s.nodesErr != nil {
		return nil, s.nodesErr
	}
	return s.nodes, nil
}

func (s stubComputer) PodsOnNode(nodeName string) ([]packing.PodInfo, error) {
	if s.podsErr != nil {
		return nil, s.podsErr
	}
	return s.pods, nil
}

type failingWriter struct {
	header http.Header
	status int
}

func (f *failingWriter) Header() http.Header {
	if f.header == nil {
		f.header = make(http.Header)
	}
	return f.header
}

func (f *failingWriter) Write(p []byte) (int, error) {
	return 0, io.ErrClosedPipe
}

func (f *failingWriter) WriteHeader(status int) {
	f.status = status
}

func TestClusterSummaryLogsHandledError(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	handler := Handler{
		Computer: stubComputer{nodesErr: errors.New("listing nodes: boom")},
		Logger:   log.New(&logs, "", 0),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cluster-summary", nil)
	rec := httptest.NewRecorder()

	handler.clusterSummary(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Log("exp:", http.StatusInternalServerError)
		t.Log("got:", rec.Code)
		t.Fatal("status code mismatch")
	}

	if !strings.Contains(logs.String(), "cluster-summary: compute node packing: listing nodes: boom") {
		t.Log("exp:", "cluster-summary log with wrapped error context")
		t.Log("got:", logs.String())
		t.Fatal("missing handled error log")
	}
}

func TestClusterSummaryLogsNodesFound(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	handler := Handler{
		Computer: stubComputer{
			nodes: []packing.NodePacking{
				{Name: "node-b", Pods: packing.PodValues{Count: 2}},
				{Name: "node-a", Pods: packing.PodValues{Count: 3}},
			},
		},
		Logger: log.New(&logs, "", 0),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cluster-summary", nil)
	rec := httptest.NewRecorder()

	handler.clusterSummary(rec, req)

	if rec.Code != http.StatusOK {
		t.Log("exp:", http.StatusOK)
		t.Log("got:", rec.Code)
		t.Fatal("status code mismatch")
	}

	if !strings.Contains(logs.String(), "cluster-summary: nodes=2 pods=5 node_names=[node-a node-b]") {
		t.Log("exp:", "cluster-summary: nodes=2 pods=5 node_names=[node-a node-b]")
		t.Log("got:", logs.String())
		t.Fatal("missing success inventory log")
	}
}

func TestNodePodsLogsHandledError(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	handler := Handler{
		Computer: stubComputer{podsErr: errors.New("listing pods for node \"node-a\": boom")},
		Logger:   log.New(&logs, "", 0),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/node-a/pods", nil)
	req.SetPathValue("name", "node-a")
	rec := httptest.NewRecorder()

	handler.nodePods(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Log("exp:", http.StatusInternalServerError)
		t.Log("got:", rec.Code)
		t.Fatal("status code mismatch")
	}

	if !strings.Contains(logs.String(), `node-pods: node="node-a": listing pods for node "node-a": boom`) {
		t.Log("exp:", `node-pods: node="node-a": listing pods for node "node-a": boom`)
		t.Log("got:", logs.String())
		t.Fatal("missing handled error log")
	}
}

func TestNodePodsLogsCount(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	handler := Handler{
		Computer: stubComputer{
			pods: []packing.PodInfo{
				{Name: "pod-1"},
				{Name: "pod-2"},
			},
		},
		Logger: log.New(&logs, "", 0),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/node-a/pods", nil)
	req.SetPathValue("name", "node-a")
	rec := httptest.NewRecorder()

	handler.nodePods(rec, req)

	if rec.Code != http.StatusOK {
		t.Log("exp:", http.StatusOK)
		t.Log("got:", rec.Code)
		t.Fatal("status code mismatch")
	}

	if !strings.Contains(logs.String(), `node-pods: node="node-a" count=2`) {
		t.Log("exp:", `node-pods: node="node-a" count=2`)
		t.Log("got:", logs.String())
		t.Fatal("missing node pod count log")
	}
}

func TestWriteJSONLogsEncodeError(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	handler := Handler{Logger: log.New(&logs, "", 0)}
	rec := httptest.NewRecorder()

	handler.writeJSON(rec, http.StatusOK, map[string]any{"bad": func() {}})

	if rec.Code != http.StatusInternalServerError {
		t.Log("exp:", http.StatusInternalServerError)
		t.Log("got:", rec.Code)
		t.Fatal("status code mismatch")
	}

	if !strings.Contains(logs.String(), "encode json response: status=200") {
		t.Log("exp:", "encode json response log")
		t.Log("got:", logs.String())
		t.Fatal("missing encode error log")
	}
}

func TestWriteJSONLogsWriteError(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	handler := Handler{Logger: log.New(&logs, "", 0)}
	writer := &failingWriter{}

	handler.writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})

	if writer.status != http.StatusOK {
		t.Log("exp:", http.StatusOK)
		t.Log("got:", writer.status)
		t.Fatal("status code mismatch")
	}

	if !strings.Contains(logs.String(), "write json response: status=200: io: read/write on closed pipe") {
		t.Log("exp:", "write json response log")
		t.Log("got:", logs.String())
		t.Fatal("missing write error log")
	}
}
