package cip

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// SendUnitData sends a connected explicit (Class 3) message on an established
// connection and returns the inner MessageRouterResponse. The caller owns the
// connection ID (from Forward_Open) and the per-connection sequence counter —
// pass a fresh, monotonically increasing seq for each request.
//
// Wire shape (Vol 2 §2-4.8): CPF [ConnectedAddress: u32 connID]
// [ConnectedData: u16 seq ‖ MR request]. The reply mirrors it; the sequence
// echo is verified against seq so a late reply can't be mistaken for the
// current one.
func (c *Client) SendUnitData(ctx context.Context, connID uint32, seq uint16, service uint8, path []byte, data []byte) (MessageRouterResponse, error) {
	mr := encodeMRRequest(service, path, data)
	connData := make([]byte, 2+len(mr))
	binary.LittleEndian.PutUint16(connData[:2], seq)
	copy(connData[2:], mr)

	addr := make([]byte, 4)
	binary.LittleEndian.PutUint32(addr, connID)

	items := []CPFItem{
		{TypeID: ItemConnectedAddress, Data: addr},
		{TypeID: ItemConnectedData, Data: connData},
	}
	// Timeout field must be zero for SendUnitData (Vol 2 §2-4.8.1).
	body := EncodeSendRRData(SendRRDataHeader{Timeout: 0}, items)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tcp == nil {
		return MessageRouterResponse{}, errors.New("cip client: not connected")
	}
	hdr := EncapHeader{Command: CmdSendUnitData, SessionHandle: c.sessHdl}
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.tcp.SetWriteDeadline(deadline)
	if _, err := c.tcp.Write(EncodeFrame(hdr, body)); err != nil {
		return MessageRouterResponse{}, err
	}
	_ = c.tcp.SetReadDeadline(deadline)
	rhdr, payload, err := c.readFrame()
	if err != nil {
		return MessageRouterResponse{}, err
	}
	if rhdr.Status != EncapStatusSuccess {
		return MessageRouterResponse{}, fmt.Errorf("encap status 0x%x", rhdr.Status)
	}
	_, ritems, err := DecodeSendRRData(payload)
	if err != nil {
		return MessageRouterResponse{}, err
	}
	if len(ritems) < 2 || ritems[1].TypeID != ItemConnectedData || len(ritems[1].Data) < 2 {
		return MessageRouterResponse{}, fmt.Errorf("unexpected connected reply CPF: %d items", len(ritems))
	}
	if echo := binary.LittleEndian.Uint16(ritems[1].Data[:2]); echo != seq {
		return MessageRouterResponse{}, fmt.Errorf("connected reply sequence mismatch: sent %d, got %d", seq, echo)
	}
	return decodeMRResponse(ritems[1].Data[2:])
}
