package uaserver

import (
	"context"
	"crypto/rsa"
	"io"
	mrand "math/rand"
	"sync"
	"time"

	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uacp"
	"github.com/gopcua/opcua/uasc"
)

type channelBroker struct {
	endpoints map[string]*ua.EndpointDescription

	wg sync.WaitGroup

	// mu protects concurrent modification of s, secureChannelID, and secureTokenID
	mu sync.RWMutex
	// s is a slice of all SecureChannels watched by the channelBroker
	s map[uint32]*uasc.SecureChannel
	// conns is the transport under each channel in s. Close closes
	// these: a SecureChannel's own Close does not close its connection,
	// so a channel's reader would stay blocked in a read until the client
	// hung up.
	conns map[uint32]*uacp.Conn
	// closed is set by Close; a connection that registers after it is
	// refused.
	closed bool

	// Next Secure Channel ID to issue to a client
	secureChannelID uint32

	// Next Token ID to issue to a client
	secureTokenID uint32

	// msgChan is the common channel that all messages from all channels
	// get funneled into for handling
	msgChan chan *uasc.MessageBody
	logger  Logger
}

func newChannelBroker(logger Logger) *channelBroker {
	rng := mrand.New(mrand.NewSource(time.Now().UnixNano()))
	return &channelBroker{
		endpoints:       make(map[string]*ua.EndpointDescription),
		s:               make(map[uint32]*uasc.SecureChannel),
		conns:           make(map[uint32]*uacp.Conn),
		msgChan:         make(chan *uasc.MessageBody),
		secureChannelID: uint32(rng.Int31()),
		secureTokenID:   uint32(rng.Int31()),
		logger:          logger,
	}
}

// RegisterConn connects a new UACP connection to the channel broker's list
// of connections and starts waiting for data on it.  Data is pushed onto the broker's
// Response channel
// Blocks until the context is done, the connection closes, or a critical error
func (c *channelBroker) RegisterConn(ctx context.Context, conn *uacp.Conn, localCert []byte, localKey *rsa.PrivateKey) error {
	cfg := defaultChannelConfig()
	cfg.Certificate = localCert
	cfg.LocalKey = localKey

	c.mu.Lock()
	c.secureChannelID++
	c.secureTokenID++
	secureChannelID := c.secureChannelID
	secureTokenID := c.secureTokenID
	sequenceNumber := uint32(mrand.Int31n(1023) + 1)
	c.mu.Unlock()

	errch := make(chan error, 1)
	sc, err := uasc.NewServerSecureChannel(
		"", // todo(fs): this is most likely wrong
		conn,
		cfg,
		errch,
		secureChannelID,
		sequenceNumber,
		secureTokenID,
	)
	if err != nil {
		if c.logger != nil {
			c.logger.Error("Error creating secure channel for new connection: %s", err)
		}
		return err
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		conn.Close()
		return io.EOF
	}
	c.s[secureChannelID] = sc
	c.conns[secureChannelID] = conn
	// Add under mu, before Close can see the channel, so Close's Wait
	// always covers every channel it closed.
	c.wg.Add(1)
	if c.logger != nil {
		c.logger.Info("Registered new channel (id %d) now at %d channels", secureChannelID, len(c.s))
	}
	c.mu.Unlock()
outer:
	for {
		select {
		case <-ctx.Done():
			// todo(fs): return error?
			if c.logger != nil {
				c.logger.Warn("Context done, closing Secure Channel %d", secureChannelID)
			}
			break outer

		default:
			msg := sc.Receive(ctx)
			if msg.Err == io.EOF {
				if c.logger != nil {
					c.logger.Warn("Secure Channel %d closed", secureChannelID)
				}
				break outer
			} else if msg.Err != nil {
				if c.logger != nil {
					c.logger.Error("Secure Channel %d error: %s", secureChannelID, msg.Err)
				}
				break outer
			}
			// The handoff honors ctx: once the server's context is
			// cancelled nothing reads msgChan any more, and a send here
			// would hold this channel (and Close's wait) forever.
			select {
			case c.msgChan <- msg:
			case <-ctx.Done():
				break outer
			}
		}
	}

	c.mu.Lock()
	delete(c.s, secureChannelID)
	delete(c.conns, secureChannelID)
	c.mu.Unlock()
	conn.Close()
	c.wg.Done()

	return nil
}

// Close closes every secure channel and the connection under it, and
// waits for each channel's reader to return. Closing the connection is
// what makes that wait short: a SecureChannel's Close only sends a
// CloseSecureChannel message, and its reader sits in a blocking read on
// the connection until the client hangs up, which a client with a
// session open does not do. gopcua's original closed only the channels,
// so a shutdown with a session open always waited out its 10 s timeout
// (nautilus#423).
func (c *channelBroker) Close() error {
	var err error
	c.mu.Lock()
	c.closed = true
	chans := make([]*uasc.SecureChannel, 0, len(c.s))
	for _, s := range c.s {
		chans = append(chans, s)
	}
	conns := make([]*uacp.Conn, 0, len(c.conns))
	for _, conn := range c.conns {
		conns = append(conns, conn)
	}
	c.mu.Unlock()
	// Outside mu: a channel's Close writes to its connection, and a
	// client that stopped reading must not hold the broker's lock.
	for _, s := range chans {
		s.Close()
	}
	for _, conn := range conns {
		conn.Close()
	}

	// Wait for all goroutines to finish or timeout
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(closeWait):
		if c.logger != nil {
			c.logger.Error("CloseAll: timed out waiting for channels to exit")
		}
	}

	return err
}

// closeWait bounds Close's wait for the channel readers. With their
// connections closed under them they return at once; this is a backstop.
const closeWait = 2 * time.Second

func (c *channelBroker) ReadMessage(ctx context.Context) *uasc.MessageBody {
	select {
	case <-ctx.Done():
		return nil
	case msg := <-c.msgChan:
		return msg
	}
}
