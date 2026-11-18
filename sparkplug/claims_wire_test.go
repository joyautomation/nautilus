// claims_wire_test.go: edge-node claims that only show against a real broker:
// ssl:// brokers connect over TLS, and bdseq-file carries the birth-death
// sequence across a restart.

package sparkplug

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/joyautomation/nautilus/internal/testmqtt"
	mqttsrv "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
)

// testBroker is an in-process broker on a plain TCP listener; it returns the
// address.
// testBroker starts an in-process broker for the package's tests, with any
// extra hooks (testmqtt.HoldTeardown) after its own. It serializes a client
// ID's sessions (testmqtt.SameIDSerializer): mochi v2.7.9 can otherwise drop
// a node's reconnected session from its client map, and the node never
// hears its primary host again.
func testBroker(t *testing.T, hooks ...mqttsrv.Hook) string {
	t.Helper()
	srv := mqttsrv.New(&mqttsrv.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	if err := srv.AddHook(&testmqtt.SameIDSerializer{Server: srv}, nil); err != nil {
		t.Fatal(err)
	}
	for _, h := range hooks {
		if err := srv.AddHook(h, nil); err != nil {
			t.Fatal(err)
		}
	}
	tcp := listeners.NewTCP(listeners.Config{ID: "plain", Address: "127.0.0.1:0"})
	if err := srv.AddListener(tcp); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		// mochi v2.7.9's Close can deadlock on a disconnecting client
		// (see sparkplug/host/mqtt_test.go closeBroker): take the listeners'
		// own client sweep out of it.
		srv.Listeners.Close("plain", func(string) {})
		done := make(chan struct{})
		go func() { _ = srv.Close(); close(done) }()
		for {
			for _, cl := range srv.Clients.GetAll() {
				if !cl.Closed() {
					_ = srv.DisconnectClient(cl, packets.ErrServerShuttingDown)
				}
			}
			select {
			case <-done:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	})
	return tcp.Address()
}

// births collects the bdSeq of every NBIRTH on the plain listener.
type births struct {
	mu    sync.Mutex
	bdSeq []int64
	ch    chan int64
}

func watchBirths(t *testing.T, plainAddr string) *births {
	t.Helper()
	b := &births{ch: make(chan int64, 16)}
	cli := mqtt.NewClient(mqtt.NewClientOptions().AddBroker("tcp://" + plainAddr).
		SetClientID("birth-watcher").SetCleanSession(true).SetOrderMatters(true))
	if tok := cli.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("watcher connect: %v", tok.Error())
	}
	t.Cleanup(func() { cli.Disconnect(50) })
	tok := cli.Subscribe("spBv1.0/Plant/NBIRTH/Edge1", 1, func(_ mqtt.Client, m mqtt.Message) {
		p, err := DecodePayload(m.Payload())
		if err != nil {
			return
		}
		for _, mt := range p.Metrics {
			if mt.Name == "bdSeq" {
				v := mt.Value.(int64)
				b.mu.Lock()
				b.bdSeq = append(b.bdSeq, v)
				b.mu.Unlock()
				b.ch <- v
			}
		}
	})
	if !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("watcher subscribe: %v", tok.Error())
	}
	return b
}

func (b *births) next(t *testing.T) int64 {
	t.Helper()
	select {
	case v := <-b.ch:
		return v
	case <-time.After(8 * time.Second):
		t.Fatal("no NBIRTH arrived")
		return 0
	}
}

func startNode(t *testing.T, broker, bdFile string) *Node {
	t.Helper()
	n, err := New(newRuntimeForGate(t), Config{BrokerURL: broker, GroupID: "Plant", EdgeNode: "Edge1", BdSeqFile: bdFile})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return n
}

// SPB-022: with bdseq-file the birth-death sequence number moves on across
// restarts; without it every run starts again at 0.
func TestBdSeqFilePersistsAcrossRestarts(t *testing.T) {
	plain := testBroker(t)
	watch := watchBirths(t, plain)
	url := "tcp://" + plain
	file := filepath.Join(t.TempDir(), "edge1.bdseq")

	for run, want := range []int64{0, 1, 2} {
		n := startNode(t, url, file)
		if got := watch.next(t); got != want {
			t.Fatalf("run %d: NBIRTH bdSeq = %d, want %d", run, got, want)
		}
		n.Stop()
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("bdseq-file was never written: %v", err)
	}

	// Without the file every run starts at 0.
	for run := 0; run < 2; run++ {
		n := startNode(t, url, "")
		if got := watch.next(t); got != 0 {
			t.Fatalf("no bdseq-file, run %d: NBIRTH bdSeq = %d, want 0", run, got)
		}
		n.Stop()
	}
}

// ── SPB-008: ssl:// connects over TLS ────────────────────────────────────

var (
	tlsOnce sync.Once
	tlsCert tls.Certificate
)

// tlsFixture makes one self-signed certificate for 127.0.0.1 per test process
// and points SSL_CERT_FILE at a copy of it in the test's own temp dir, so the
// node's default verification (system roots; the node has no TLS options)
// trusts it. Go reads the roots once per process, on first use, so the same
// certificate is reused across -count runs; the file is removed with the
// temp dir and the variable restored when the test ends.
func tlsFixture(t *testing.T) *tls.Config {
	t.Helper()
	tlsOnce.Do(func() {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		tmpl := &x509.Certificate{
			SerialNumber:          big.NewInt(1),
			Subject:               pkix.Name{CommonName: "nautilus test broker"},
			NotBefore:             time.Now().Add(-time.Hour),
			NotAfter:              time.Now().Add(24 * time.Hour),
			KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
			ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			BasicConstraintsValid: true,
			IsCA:                  true,
			IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		tlsCert = tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	})
	if len(tlsCert.Certificate) == 0 {
		t.Fatal("no test certificate")
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsCert.Certificate[0]})
	if err := os.WriteFile(ca, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", ca)
	return &tls.Config{Certificates: []tls.Certificate{tlsCert}}
}

func TestEdgeNodeConnectsOverTLS(t *testing.T) {
	cfg := tlsFixture(t)
	srv := mqttsrv.New(&mqttsrv.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	plain := listeners.NewTCP(listeners.Config{ID: "plain", Address: "127.0.0.1:0"})
	secure := listeners.NewTCP(listeners.Config{ID: "tls", Address: "127.0.0.1:0", TLSConfig: cfg})
	for _, l := range []*listeners.TCP{plain, secure} {
		if err := srv.AddListener(l); err != nil {
			t.Fatal(err)
		}
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		srv.Listeners.Close("plain", func(string) {})
		srv.Listeners.Close("tls", func(string) {})
		done := make(chan struct{})
		go func() { _ = srv.Close(); close(done) }()
		for {
			for _, cl := range srv.Clients.GetAll() {
				if !cl.Closed() {
					_ = srv.DisconnectClient(cl, packets.ErrServerShuttingDown)
				}
			}
			select {
			case <-done:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	})

	watch := watchBirths(t, plain.Address())
	n := startNode(t, "ssl://"+secure.Address(), "")
	defer n.Stop()
	if got := watch.next(t); got != 0 {
		t.Errorf("NBIRTH bdSeq = %d, want 0", got)
	}
	if st := n.Status(); !st.Connected || st.Broker != "ssl://"+secure.Address() {
		t.Errorf("status = %+v, want connected over ssl://", st)
	}
}
