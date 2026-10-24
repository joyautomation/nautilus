package plcnext

// gds.go is the Global Data Space client: the firmware's
// Arp.Plc.Gds.Services.IDataAccessService over gRPC on the local Unix
// socket, behind a small interface so the driver and the CLI are tested
// against an in-process fake.
//
// What the service will and will not do, as measured on an AXC F 2152 at
// firmware 2023.0 (the brief has the evidence): it resolves ports of
// MainProcess components — eCLR variables, PLM component ports — and NOT
// the I/O components' own ports (Arp.Io.AxlC/…, Arp.Io.PnC/… answer
// DAE_NotExists), so the process image has to be mirrored into a
// MainProcess port by a GDS connector first; and it reads a scalar or one
// array ELEMENT by name (`Port[17]`) but answers DAE_NotSupported for an
// array as a whole. So the image crosses the socket as one Read of N
// element names per scan and one Write of the dirty span — still a single
// round trip each way, which is what the scan rate is paid in.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	arptypes "github.com/joyautomation/nautilus/plcnext/gen/arptypes"
	gdspb "github.com/joyautomation/nautilus/plcnext/gen/plc/gds"
)

// dataAccess is what the driver needs of the GDS.
type dataAccess interface {
	// ReadBytes reads elements port[0] … port[n-1] of a byte-array port,
	// plus any extra scalar ports as booleans, in one round trip.
	ReadBytes(ctx context.Context, port string, n int, bools ...string) ([]byte, []bool, error)
	// WriteBytes writes data to port[offset] … in one round trip.
	WriteBytes(ctx context.Context, port string, offset int, data []byte) error
	ReadBool(ctx context.Context, port string) (bool, error)
	WriteBool(ctx context.Context, port string, v bool) error
	ReadNumber(ctx context.Context, port string) (float64, error)
	Close() error
}

// Client is the real dataAccess over gRPC.
type Client struct {
	conn *grpc.ClientConn
	svc  gdspb.IDataAccessServiceClient
}

// Dial connects to the firmware's gRPC socket. target is a grpc target:
// "unix:///run/plcnext/grpc.sock" for the controller, or any host:port a
// test server listens on.
func Dial(ctx context.Context, target string) (*Client, error) {
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("plcnext: %w", err)
	}
	return &Client{conn: conn, svc: gdspb.NewIDataAccessServiceClient(conn)}, nil
}

// SocketTarget renders a Unix socket path as a grpc target.
func SocketTarget(path string) string { return "unix://" + path }

func (c *Client) Close() error { return c.conn.Close() }

// PortError is the firmware's own verdict on one port name.
type PortError struct {
	Port string
	Code string
}

func (e PortError) Error() string { return fmt.Sprintf("plcnext: %s: %s", e.Port, e.Code) }

// element names one array element the way the service resolves it.
func element(port string, i int) string { return port + "[" + strconv.Itoa(i) + "]" }

func (c *Client) read(ctx context.Context, port string) (*arptypes.ObjectType, error) {
	resp, err := c.svc.ReadSingle(ctx, &gdspb.IDataAccessServiceReadSingleRequest{PortName: port})
	if err != nil {
		return nil, fmt.Errorf("plcnext: read %s: %w", port, err)
	}
	item := resp.GetXReturnValue()
	if item == nil {
		return nil, fmt.Errorf("plcnext: read %s: empty reply", port)
	}
	if item.GetError() != gdspb.DataAccessError_DAE_None {
		return nil, PortError{Port: port, Code: item.GetError().String()}
	}
	return item.GetValue(), nil
}

// readMany reads several names in one round trip, in order.
func (c *Client) readMany(ctx context.Context, names []string) ([]*arptypes.ObjectType, error) {
	resp, err := c.svc.Read(ctx, &gdspb.IDataAccessServiceReadRequest{PortNames: names})
	if err != nil {
		return nil, fmt.Errorf("plcnext: read %d ports: %w", len(names), err)
	}
	items := resp.GetXReturnValue()
	if len(items) != len(names) {
		return nil, fmt.Errorf("plcnext: read %d ports: %d replies", len(names), len(items))
	}
	out := make([]*arptypes.ObjectType, len(items))
	for i, item := range items {
		if item.GetError() != gdspb.DataAccessError_DAE_None {
			return nil, PortError{Port: names[i], Code: item.GetError().String()}
		}
		out[i] = item.GetValue()
	}
	return out, nil
}

func (c *Client) write(ctx context.Context, port string, v *arptypes.ObjectType) error {
	resp, err := c.svc.WriteSingle(ctx, &gdspb.IDataAccessServiceWriteSingleRequest{Data: &gdspb.WriteItem{PortName: port, Value: v}})
	if err != nil {
		return fmt.Errorf("plcnext: write %s: %w", port, err)
	}
	if code := resp.GetXReturnValue(); code != gdspb.DataAccessError_DAE_None {
		return PortError{Port: port, Code: code.String()}
	}
	return nil
}

// ReadBytes reads port[0..n) and the given boolean ports in one round trip.
func (c *Client) ReadBytes(ctx context.Context, port string, n int, bools ...string) ([]byte, []bool, error) {
	names := make([]string, 0, n+len(bools))
	for i := 0; i < n; i++ {
		names = append(names, element(port, i))
	}
	names = append(names, bools...)
	vals, err := c.readMany(ctx, names)
	if err != nil {
		return nil, nil, err
	}
	data := make([]byte, n)
	for i := 0; i < n; i++ {
		data[i] = byte(numberOf(vals[i]))
	}
	flags := make([]bool, len(bools))
	for i := range bools {
		flags[i] = vals[n+i].GetBoolValue()
	}
	return data, flags, nil
}

// WriteBytes writes data to port[offset..offset+len(data)) in one round trip.
func (c *Client) WriteBytes(ctx context.Context, port string, offset int, data []byte) error {
	items := make([]*gdspb.WriteItem, len(data))
	for i, b := range data {
		items[i] = &gdspb.WriteItem{PortName: element(port, offset+i), Value: uint8Value(b)}
	}
	resp, err := c.svc.Write(ctx, &gdspb.IDataAccessServiceWriteRequest{Data: items})
	if err != nil {
		return fmt.Errorf("plcnext: write %s[%d..%d]: %w", port, offset, offset+len(data), err)
	}
	for i, code := range resp.GetXReturnValue() {
		if code != gdspb.DataAccessError_DAE_None {
			return PortError{Port: items[i].PortName, Code: code.String()}
		}
	}
	return nil
}

func uint8Value(b byte) *arptypes.ObjectType {
	return &arptypes.ObjectType{TypeCode: arptypes.CoreType_CT_Uint8, Value: &arptypes.ObjectType_Uint8Value{Uint8Value: uint32(b)}}
}

// ReadBool reads a BOOL port.
func (c *Client) ReadBool(ctx context.Context, port string) (bool, error) {
	v, err := c.read(ctx, port)
	if err != nil {
		return false, err
	}
	return v.GetBoolValue(), nil
}

// WriteBool writes a BOOL port.
func (c *Client) WriteBool(ctx context.Context, port string, b bool) error {
	return c.write(ctx, port, &arptypes.ObjectType{TypeCode: arptypes.CoreType_CT_Boolean, Value: &arptypes.ObjectType_BoolValue{BoolValue: b}})
}

// ReadNumber reads any integer or real port as a float64.
func (c *Client) ReadNumber(ctx context.Context, port string) (float64, error) {
	v, err := c.read(ctx, port)
	if err != nil {
		return 0, err
	}
	return numberOf(v), nil
}

// Describe reads a port of any type and renders what the firmware
// returned — its type code and value — for commissioning: `naut plcnext
// read` uses it to find out what a GDS name actually is before a
// manifest binds it.
func (c *Client) Describe(ctx context.Context, port string) (string, error) {
	v, err := c.read(ctx, port)
	if err != nil {
		return "", err
	}
	if arr := v.GetArrayValue(); arr != nil {
		n := len(arr.ArrayElements)
		if n == 0 {
			return fmt.Sprintf("%s[0]", v.GetTypeCode()), nil
		}
		head := min(n, 16)
		parts := make([]string, head)
		for i := range parts {
			parts[i] = fmt.Sprint(scalarOf(arr.ArrayElements[i]))
		}
		more := ""
		if n > head {
			more = " …"
		}
		return fmt.Sprintf("%s[%d] = [%s%s]", arr.ArrayElements[0].GetTypeCode(), n, strings.Join(parts, " "), more), nil
	}
	return fmt.Sprintf("%s = %v", v.GetTypeCode(), scalarOf(v)), nil
}

// WriteScalar writes text to a scalar port as the port's own type: it
// reads the port first to learn the type code, then converts. For
// `naut plcnext write`.
func (c *Client) WriteScalar(ctx context.Context, port, text string) error {
	cur, err := c.read(ctx, port)
	if err != nil {
		return err
	}
	v := &arptypes.ObjectType{TypeCode: cur.GetTypeCode()}
	switch cur.GetValue().(type) {
	case *arptypes.ObjectType_BoolValue:
		b, err := strconv.ParseBool(text)
		if err != nil {
			return fmt.Errorf("plcnext: %s is a BOOL; %q is not", port, text)
		}
		v.Value = &arptypes.ObjectType_BoolValue{BoolValue: b}
	case *arptypes.ObjectType_StringValue:
		v.Value = &arptypes.ObjectType_StringValue{StringValue: text}
	case *arptypes.ObjectType_FloatValue, *arptypes.ObjectType_DoubleValue:
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return fmt.Errorf("plcnext: %s is a real; %q is not", port, text)
		}
		if _, ok := cur.GetValue().(*arptypes.ObjectType_FloatValue); ok {
			v.Value = &arptypes.ObjectType_FloatValue{FloatValue: float32(f)}
		} else {
			v.Value = &arptypes.ObjectType_DoubleValue{DoubleValue: f}
		}
	default:
		n, err := strconv.ParseInt(text, 0, 64)
		if err != nil {
			return fmt.Errorf("plcnext: %s is an integer; %q is not", port, text)
		}
		switch cur.GetValue().(type) {
		case *arptypes.ObjectType_Int8Value:
			v.Value = &arptypes.ObjectType_Int8Value{Int8Value: int32(n)}
		case *arptypes.ObjectType_Uint8Value:
			v.Value = &arptypes.ObjectType_Uint8Value{Uint8Value: uint32(n)}
		case *arptypes.ObjectType_Int16Value:
			v.Value = &arptypes.ObjectType_Int16Value{Int16Value: int32(n)}
		case *arptypes.ObjectType_Uint16Value:
			v.Value = &arptypes.ObjectType_Uint16Value{Uint16Value: uint32(n)}
		case *arptypes.ObjectType_Int32Value:
			v.Value = &arptypes.ObjectType_Int32Value{Int32Value: int32(n)}
		case *arptypes.ObjectType_Uint32Value:
			v.Value = &arptypes.ObjectType_Uint32Value{Uint32Value: uint32(n)}
		case *arptypes.ObjectType_Int64Value:
			v.Value = &arptypes.ObjectType_Int64Value{Int64Value: n}
		case *arptypes.ObjectType_UIntValue:
			v.Value = &arptypes.ObjectType_UIntValue{UIntValue: uint64(n)}
		default:
			return fmt.Errorf("plcnext: %s: cannot write a %s from text", port, cur.GetTypeCode())
		}
	}
	return c.write(ctx, port, v)
}

// scalarOf is the Go value of a scalar ObjectType, for printing.
func scalarOf(v *arptypes.ObjectType) any {
	switch x := v.GetValue().(type) {
	case *arptypes.ObjectType_BoolValue:
		return x.BoolValue
	case *arptypes.ObjectType_StringValue:
		return x.StringValue
	case *arptypes.ObjectType_Int8Value, *arptypes.ObjectType_Uint8Value, *arptypes.ObjectType_Int16Value,
		*arptypes.ObjectType_Uint16Value, *arptypes.ObjectType_Int32Value, *arptypes.ObjectType_Uint32Value,
		*arptypes.ObjectType_Int64Value, *arptypes.ObjectType_UIntValue, *arptypes.ObjectType_FloatValue,
		*arptypes.ObjectType_DoubleValue:
		return numberOf(v)
	}
	return v.GetValue()
}

func numberOf(v *arptypes.ObjectType) float64 {
	switch x := v.GetValue().(type) {
	case *arptypes.ObjectType_BoolValue:
		if x.BoolValue {
			return 1
		}
		return 0
	case *arptypes.ObjectType_Int8Value:
		return float64(x.Int8Value)
	case *arptypes.ObjectType_Uint8Value:
		return float64(x.Uint8Value)
	case *arptypes.ObjectType_Int16Value:
		return float64(x.Int16Value)
	case *arptypes.ObjectType_Uint16Value:
		return float64(x.Uint16Value)
	case *arptypes.ObjectType_Int32Value:
		return float64(x.Int32Value)
	case *arptypes.ObjectType_Uint32Value:
		return float64(x.Uint32Value)
	case *arptypes.ObjectType_Int64Value:
		return float64(x.Int64Value)
	case *arptypes.ObjectType_UIntValue:
		return float64(x.UIntValue)
	case *arptypes.ObjectType_FloatValue:
		return float64(x.FloatValue)
	case *arptypes.ObjectType_DoubleValue:
		return x.DoubleValue
	}
	return 0
}

// withTimeout is the per-call deadline every GDS round trip gets.
func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}
