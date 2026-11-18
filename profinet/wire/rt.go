package wire

import (
	"encoding/binary"
	"fmt"
)

// RT cyclic frames (RT_CLASS_1). The Ethernet payload of a cyclic frame is
//
//	FrameID(2) | C-SDU | CycleCounter(2) | DataStatus(1) | TransferStatus(1)
//
// The C-SDU holds every IODataObject of the IOCR at the FrameOffset the
// controller assigned in Connect: the submodule's data, then its IOPS
// (provider status). The IOCS entries (consumer status, for the other
// direction's data) sit at their own FrameOffsets. A C-SDU is at least
// MinCSDU bytes; the sender pads.

// MinCSDU is the shortest C-SDU on the wire (the frame is padded to the
// Ethernet minimum).
const MinCSDU = 40

// rtTrailer is CycleCounter + DataStatus + TransferStatus.
const rtTrailer = 4

// IsRTCyclicFrame reports whether frameID is in the RT_CLASS_1/2 cyclic
// range.
func IsRTCyclicFrame(frameID uint16) bool {
	return frameID >= 0x0100 && frameID <= 0xF7FF
}

// CyclicFrame is one decoded cyclic frame. CSDU aliases the frame.
type CyclicFrame struct {
	FrameID        uint16
	CSDU           []byte
	CycleCounter   uint16
	DataStatus     uint8
	TransferStatus uint8
}

// ParseCyclic decodes the Ethernet payload of a cyclic frame (from the
// FrameID on). Trailing Ethernet padding cannot be told apart from the
// C-SDU here: the receiver knows the IOCR's DataLength and passes
// csduLen; csduLen < 0 takes everything before the trailer.
func ParseCyclic(payload []byte, csduLen int) (CyclicFrame, error) {
	if len(payload) < 2+rtTrailer {
		return CyclicFrame{}, errShort("RT frame", 2+rtTrailer, len(payload))
	}
	n := len(payload) - 2 - rtTrailer
	if csduLen >= 0 {
		if 2+csduLen+rtTrailer > len(payload) {
			return CyclicFrame{}, errShort("RT frame C-SDU", 2+csduLen+rtTrailer, len(payload))
		}
		n = csduLen
	}
	t := payload[2+n:]
	return CyclicFrame{
		FrameID:        binary.BigEndian.Uint16(payload),
		CSDU:           payload[2 : 2+n],
		CycleCounter:   binary.BigEndian.Uint16(t),
		DataStatus:     t[2],
		TransferStatus: t[3],
	}, nil
}

// AppendCyclic appends a cyclic frame's Ethernet payload to dst. A csdu
// shorter than MinCSDU is zero-padded.
func AppendCyclic(dst []byte, frameID uint16, csdu []byte, cycleCounter uint16, dataStatus uint8) []byte {
	dst = binary.BigEndian.AppendUint16(dst, frameID)
	dst = append(dst, csdu...)
	for i := len(csdu); i < MinCSDU; i++ {
		dst = append(dst, 0)
	}
	dst = binary.BigEndian.AppendUint16(dst, cycleCounter)
	return append(dst, dataStatus, 0x00)
}

// DataStatusMeaning is what a consumer may conclude from a frame's
// DataStatus about the data in it.
type DataStatusMeaning uint8

const (
	// StatusRun: primary, valid, provider in run — the data is a live
	// value from a running provider.
	StatusRun DataStatusMeaning = iota
	// StatusStop: the provider says it is in stop (a PLC in STOP, or a
	// controller whose outputs are not being commanded). The bytes are
	// the provider's substitute or last values, not live commands.
	StatusStop
	// StatusInvalid: DataValid clear, backup, or Ignore set — the frame
	// carries no usable data.
	StatusInvalid
)

// MeaningOf classifies a DataStatus byte. A station problem with an
// otherwise running, valid frame is still StatusRun: it is a diagnosis
// flag, and the per-submodule IOPS says which data is bad.
func MeaningOf(ds uint8) DataStatusMeaning {
	switch {
	case ds&DataStatusIgnore != 0, ds&DataStatusValid == 0, ds&DataStatusPrimary == 0:
		return StatusInvalid
	case ds&DataStatusRun == 0:
		return StatusStop
	}
	return StatusRun
}

// IOxSGoodState reports whether an IOPS/IOCS byte says DataState good
// (bit 7). The extension bit (0) chains further bytes, which RT_CLASS_1
// IOCRs do not use; only the first byte is considered.
func IOxSGoodState(b uint8) bool { return b&0x80 != 0 }

// RTA (acyclic real-time: alarms and their acknowledgements).
const (
	FrameIDAlarmHigh uint16 = 0xFC01
	FrameIDAlarmLow  uint16 = 0xFE01

	// RTA PDU types (low nibble of the PDU type byte).
	RTAPDUTypeData byte = 0x01
	RTAPDUTypeNACK byte = 0x02
	RTAPDUTypeACK  byte = 0x03
	RTAPDUTypeERR  byte = 0x04

	rtaPDUVersion byte = 0x01 // high nibble of the PDU type byte
	// RTASendSeqNone is the send sequence number of an ACK sent before any
	// data PDU.
	RTASendSeqNone uint16 = 0xFFFE

	// rtaHeaderLen is FrameID + dst ref, src ref, PDU type, add flags,
	// send seq, ack seq, var part length.
	rtaHeaderLen = 14
)

// Alarm types.
const (
	AlarmTypeDiagnosis   uint16 = 0x0001
	AlarmTypeProcess     uint16 = 0x0002
	AlarmTypePull        uint16 = 0x0006
	AlarmTypePlug        uint16 = 0x0007
	AlarmTypeStatus      uint16 = 0x0008
	AlarmTypeUpdate      uint16 = 0x0009
	AlarmTypeReturnOfSub uint16 = 0x000B
)

// IsAlarmFrame reports whether frameID is an RTA channel.
func IsAlarmFrame(frameID uint16) bool {
	return frameID == FrameIDAlarmHigh || frameID == FrameIDAlarmLow
}

// RTAPDU is one decoded RTA PDU. Payload aliases the frame.
type RTAPDU struct {
	FrameID         uint16
	DstRef, SrcRef  uint16
	PDUType         byte // low nibble only
	AddFlags        byte
	SendSeq, AckSeq uint16
	Payload         []byte
}

// ParseRTA decodes an RTA PDU from the Ethernet payload (FrameID on).
func ParseRTA(payload []byte) (RTAPDU, error) {
	if len(payload) < rtaHeaderLen {
		return RTAPDU{}, errShort("RTA PDU", rtaHeaderLen, len(payload))
	}
	n := int(binary.BigEndian.Uint16(payload[12:14]))
	if len(payload) < rtaHeaderLen+n {
		return RTAPDU{}, errShort("RTA var part", rtaHeaderLen+n, len(payload))
	}
	return RTAPDU{
		FrameID:  binary.BigEndian.Uint16(payload[0:2]),
		DstRef:   binary.BigEndian.Uint16(payload[2:4]),
		SrcRef:   binary.BigEndian.Uint16(payload[4:6]),
		PDUType:  payload[6] & 0x0F,
		AddFlags: payload[7],
		SendSeq:  binary.BigEndian.Uint16(payload[8:10]),
		AckSeq:   binary.BigEndian.Uint16(payload[10:12]),
		Payload:  payload[rtaHeaderLen : rtaHeaderLen+n],
	}, nil
}

// BuildRTA assembles an RTA PDU (window size 1, no TACK).
func BuildRTA(frameID, dstRef, srcRef uint16, pduType byte, sendSeq, ackSeq uint16, payload []byte) []byte {
	buf := make([]byte, rtaHeaderLen+len(payload))
	binary.BigEndian.PutUint16(buf[0:2], frameID)
	binary.BigEndian.PutUint16(buf[2:4], dstRef)
	binary.BigEndian.PutUint16(buf[4:6], srcRef)
	buf[6] = pduType&0x0F | rtaPDUVersion<<4
	buf[7] = 0x01
	binary.BigEndian.PutUint16(buf[8:10], sendSeq)
	binary.BigEndian.PutUint16(buf[10:12], ackSeq)
	binary.BigEndian.PutUint16(buf[12:14], uint16(len(payload)))
	copy(buf[rtaHeaderLen:], payload)
	return buf
}

// BuildAlarmAck acknowledges data PDU ackSeq on frameID's channel.
func BuildAlarmAck(frameID, dstRef, srcRef, ackSeq uint16) []byte {
	return BuildRTA(frameID, dstRef, srcRef, RTAPDUTypeACK, RTASendSeqNone, ackSeq, nil)
}

func errShort(what string, want, got int) error {
	return fmt.Errorf("profinet: %s: need %d bytes, have %d", what, want, got)
}
