package ir

// ─── READ / WRITE: IEC 61131-5 data acquisition on a channel ───────
//
// A program reads or writes one variable on another device through a
// channel the manifest declares (`channels:` in nautilus.yaml). The block
// never touches the network: it posts a request and polls for the outcome
// through the Channels Host extension, so the scan never waits on a peer.
// docs/design/logix-authoring.md, "MSG as IEC 61131-5 READ/WRITE", is the
// design; the pins follow 61131-5 with two departures it explains: RD_1 is
// VAR_IN_OUT (written only by the call that completes), and BUSY is added.
//
//	READ   in: REQ BOOL, ID STRING, VAR_1 STRING   in-out: RD_1 ANY
//	       out: NDR, ERROR, BUSY BOOL, STATUS INT
//	WRITE  in: REQ BOOL, ID STRING, VAR_1 STRING, SD_1 ANY
//	       out: DONE, ERROR, BUSY BOOL, STATUS INT
//
// A REQ rising edge while idle clears the outputs, sets BUSY and posts the
// request (a WRITE posts SD_1 as it is at that call). Each call while BUSY
// polls; the call that sees the outcome clears BUSY and sets NDR/DONE, or
// ERROR and STATUS. A READ writes RD_1 only on success, whole. NDR, DONE,
// ERROR and STATUS then hold until the next REQ rising edge. A REQ edge
// while BUSY is ignored, and REQ falling does not cancel a request.

// AnyT is the type of an ANY pin on a built-in block (RD_1, SD_1): the pin
// takes whatever type the call binds to it, and the block carries the value
// as it is. Only built-in blocks declare it.
var AnyT = &Type{Kind: TypeVoid, Name: "ANY"}

// IsAny reports whether t is AnyT.
func IsAny(t *Type) bool { return t != nil && t.Kind == TypeVoid && t.Name == "ANY" }

// STATUS values READ and WRITE report that are not the peer's. Zero is
// success; a positive STATUS is the status code the peer's protocol
// returned (CIP's general status on a CIP channel).
const (
	CommOK          = 0
	CommNoChannel   = -1 // ID names no channel this host has
	CommUnreachable = -2 // the channel could not reach its peer
	CommTimeout     = -3 // no reply within the channel's timeout
	CommTypeClash   = -4 // the peer's value does not fit the local variable
	CommQueueFull   = -5 // the channel already holds its limit of requests
	CommLost        = -6 // the request is gone (a restart or a takeover)
)

// CommResult is a finished request: its STATUS and, for a successful READ,
// the value converted to the shape of the variable the request named.
type CommResult struct {
	Status int
	Value  Value
}

// Channels is an optional Host extension: the communication channels READ
// and WRITE run on. Every method returns at once. Post* hand a request to
// the channel and return a ticket (> 0), or 0 and the STATUS that stopped
// it (CommNoChannel, CommQueueFull). Poll reports a ticket's outcome:
// done false while it is in flight, and a ticket the host does not know is
// done with CommLost. A finished ticket is forgotten once polled.
//
// PostRead's shape is the current value of the variable the reply lands
// in; the channel converts the peer's value into that shape, off the scan.
type Channels interface {
	PostRead(channel, variable string, shape Value) (ticket int64, status int)
	PostWrite(channel, variable string, data Value) (ticket int64, status int)
	Poll(ticket int64) (res CommResult, done bool)
}

func init() {
	registerRead()
	registerWrite()
}

var statusT = intTypes["INT"]

// READ slots, in Inputs ‖ Outputs ‖ InOuts ‖ Internals order.
const (
	rdREQ = iota
	rdID
	rdVAR
	rdNDR
	rdERROR
	rdBUSY
	rdSTATUS
	rdRD
	rdPrevREQ
	rdTicket
)

func registerRead() {
	RegisterFB(&FBDef{
		Name:      "READ",
		Inputs:    []FBSlot{{Name: "REQ", Type: BoolT}, {Name: "ID", Type: StringT}, {Name: "VAR_1", Type: StringT}},
		Outputs:   []FBSlot{{Name: "NDR", Type: BoolT}, {Name: "ERROR", Type: BoolT}, {Name: "BUSY", Type: BoolT}, {Name: "STATUS", Type: statusT}},
		InOuts:    []FBSlot{{Name: "RD_1", Type: AnyT}},
		Internals: []FBSlot{{Name: "_prevREQ", Type: BoolT}, {Name: "_ticket", Type: IntT}},
		Step: func(inst *FBInstance, ctx FBStepCtx) error {
			s := inst.Slots
			edge := s[rdREQ].B && !s[rdPrevREQ].B
			s[rdPrevREQ] = BoolVal(s[rdREQ].B)
			if s[rdBUSY].B {
				res, done := pollComm(ctx, s[rdTicket].I)
				if !done {
					return nil
				}
				s[rdBUSY], s[rdTicket] = BoolVal(false), IntVal(0)
				if res.Status == CommOK && !sameValueShape(res.Value, s[rdRD]) {
					res.Status = CommTypeClash
				}
				if res.Status != CommOK {
					s[rdERROR], s[rdSTATUS] = BoolVal(true), IntVal(int64(res.Status))
					return nil
				}
				s[rdRD] = res.Value
				s[rdNDR] = BoolVal(true)
				return nil
			}
			if !edge {
				return nil
			}
			s[rdNDR], s[rdERROR], s[rdSTATUS] = BoolVal(false), BoolVal(false), IntVal(0)
			ch, ok := ctx.Host.(Channels)
			if !ok {
				s[rdERROR], s[rdSTATUS] = BoolVal(true), IntVal(CommNoChannel)
				return nil
			}
			ticket, st := ch.PostRead(s[rdID].S, s[rdVAR].S, CopyValue(s[rdRD]))
			startComm(s, rdBUSY, rdERROR, rdSTATUS, rdTicket, ticket, st)
			return nil
		},
	})
}

// WRITE slots, in Inputs ‖ Outputs ‖ InOuts ‖ Internals order.
const (
	wrREQ = iota
	wrID
	wrVAR
	wrSD
	wrDONE
	wrERROR
	wrBUSY
	wrSTATUS
	wrPrevREQ
	wrTicket
)

func registerWrite() {
	RegisterFB(&FBDef{
		Name:      "WRITE",
		Inputs:    []FBSlot{{Name: "REQ", Type: BoolT}, {Name: "ID", Type: StringT}, {Name: "VAR_1", Type: StringT}, {Name: "SD_1", Type: AnyT}},
		Outputs:   []FBSlot{{Name: "DONE", Type: BoolT}, {Name: "ERROR", Type: BoolT}, {Name: "BUSY", Type: BoolT}, {Name: "STATUS", Type: statusT}},
		Internals: []FBSlot{{Name: "_prevREQ", Type: BoolT}, {Name: "_ticket", Type: IntT}},
		Step: func(inst *FBInstance, ctx FBStepCtx) error {
			s := inst.Slots
			edge := s[wrREQ].B && !s[wrPrevREQ].B
			s[wrPrevREQ] = BoolVal(s[wrREQ].B)
			if s[wrBUSY].B {
				res, done := pollComm(ctx, s[wrTicket].I)
				if !done {
					return nil
				}
				s[wrBUSY], s[wrTicket] = BoolVal(false), IntVal(0)
				if res.Status != CommOK {
					s[wrERROR], s[wrSTATUS] = BoolVal(true), IntVal(int64(res.Status))
					return nil
				}
				s[wrDONE] = BoolVal(true)
				return nil
			}
			if !edge {
				return nil
			}
			s[wrDONE], s[wrERROR], s[wrSTATUS] = BoolVal(false), BoolVal(false), IntVal(0)
			ch, ok := ctx.Host.(Channels)
			if !ok {
				s[wrERROR], s[wrSTATUS] = BoolVal(true), IntVal(CommNoChannel)
				return nil
			}
			ticket, st := ch.PostWrite(s[wrID].S, s[wrVAR].S, CopyValue(s[wrSD]))
			startComm(s, wrBUSY, wrERROR, wrSTATUS, wrTicket, ticket, st)
			return nil
		},
	})
}

// startComm records a post's outcome: BUSY with its ticket, or ERROR with
// the STATUS that refused it.
func startComm(s []Value, busy, errIdx, status, ticketIdx int, ticket int64, st int) {
	if ticket <= 0 {
		if st == CommOK {
			st = CommLost
		}
		s[errIdx], s[status] = BoolVal(true), IntVal(int64(st))
		return
	}
	s[busy], s[ticketIdx] = BoolVal(true), IntVal(ticket)
}

// pollComm polls a ticket; a host that lost its channels (or never had
// them) reports it lost.
func pollComm(ctx FBStepCtx, ticket int64) (CommResult, bool) {
	ch, ok := ctx.Host.(Channels)
	if !ok || ticket <= 0 {
		return CommResult{Status: CommLost}, true
	}
	return ch.Poll(ticket)
}

// sameValueShape reports whether a and b are values of one type shape: the same
// kind, the same UDT, arrays of the same length with elements alike. A READ
// lands its reply only when it still fits the variable it names, which an
// online edit may have changed while the request was in flight.
func sameValueShape(a, b Value) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case TypeStruct:
		return a.Struct == b.Struct && len(a.Fld) == len(b.Fld)
	case TypeArray:
		if len(a.Arr) != len(b.Arr) {
			return false
		}
		for i := range a.Arr {
			if !sameValueShape(a.Arr[i], b.Arr[i]) {
				return false
			}
		}
	}
	return true
}

// CommCall is a READ or WRITE call whose ID is a constant: the channel it
// names, which `naut check` resolves against the manifest's channels.
type CommCall struct {
	Block string // READ or WRITE
	ID    string
}

// CommCalls lists the READ and WRITE calls in body whose ID is a constant
// string. A computed ID is left to run time, where an unknown one reports
// CommNoChannel.
func CommCalls(body []Stmt) []CommCall {
	var out []CommCall
	var walk func([]Stmt)
	walk = func(b []Stmt) {
		for _, s := range b {
			switch n := s.(type) {
			case *FBCall:
				if n.Def == nil || (n.Def.Name != "READ" && n.Def.Name != "WRITE") {
					continue
				}
				for _, in := range n.Inputs {
					if in.SlotIdx != rdID { // ID is slot 1 on both blocks
						continue
					}
					if l, ok := in.Value.(*Lit); ok && l.V.Kind == TypeString {
						out = append(out, CommCall{Block: n.Def.Name, ID: l.V.S})
					}
				}
			case *If:
				walk(n.Then)
				walk(n.Else)
			case *For:
				walk(n.Body)
			case *While:
				walk(n.Body)
			case *Repeat:
				walk(n.Body)
			case *Case:
				for _, c := range n.Clauses {
					walk(c.Body)
				}
				walk(n.Else)
			}
		}
	}
	walk(body)
	return out
}
