// Package k8sapi is a minimal Kubernetes API client for code running inside
// a pod: the mounted service account's CA and namespace, and its bearer
// token re-read on every request so token rotation never strands a caller.
// Pure stdlib — the callers (retain's ConfigMap store, leader's Lease
// elector) speak to two tiny corners of the API and do not need client-go.
package k8sapi

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"
	"time"
)

const saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// InCluster reports whether this process runs inside a Kubernetes pod —
// the signal the retain and leader packages use to pick an implementation.
func InCluster() bool { return os.Getenv("KUBERNETES_SERVICE_HOST") != "" }

// Client speaks to the API server. Zero-value fields are filled by New for
// in-cluster use; tests construct one directly against an httptest server.
type Client struct {
	Base      string // https://host:port, no trailing slash
	Namespace string
	HTTP      *http.Client
	tokenPath string // re-read per request; empty sends no Authorization
}

// New builds a client from the pod's mounted service account.
func New() (*Client, error) {
	ns, err := os.ReadFile(saDir + "/namespace")
	if err != nil {
		return nil, err
	}
	caPem, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPem)
	return &Client{
		Base:      "https://" + os.Getenv("KUBERNETES_SERVICE_HOST") + ":" + os.Getenv("KUBERNETES_SERVICE_PORT"),
		Namespace: string(bytes.TrimSpace(ns)),
		HTTP: &http.Client{
			Timeout:   3 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
		},
		tokenPath: saDir + "/token",
	}, nil
}

// Do issues one request. path is everything after Base (starts with "/").
// The bearer token is read fresh each call so a rotated token takes effect
// on the next request rather than the next restart.
func (c *Client) Do(method, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest(method, c.Base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if c.tokenPath != "" {
		if tok, err := os.ReadFile(c.tokenPath); err == nil {
			req.Header.Set("Authorization", "Bearer "+string(bytes.TrimSpace(tok)))
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.HTTP.Do(req)
}
