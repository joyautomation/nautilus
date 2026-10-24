package plcnext

// gds.go is the Global Data Space client: the firmware's
// Arp.Plc.Gds.Services.IDataAccessService over gRPC on the local Unix
// socket, behind a small interface so the driver and the CLI are tested
// against an in-process fake. OctetstringN ports (the BusConductor images)
// travel as an array of Uint8 ObjectTypes — one protobuf message per byte,
// which is why the driver reads the image in one call and never per tag.

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	arptypes "github.com/joyautomation/nautilus/plcnext/gen/arptypes"
	gdspb "github.com/joyautomation/nautilus/plcnext/gen/plc/gds"
)

// dataAccess is what the driver needs of the GDS.
type dataAccess interface {
	ReadBytes(ctx context.Context, port string) ([]byte, error)
	WriteBytes(ctx context.Context, port string, data []byte) error
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

func (c *Client) write(ctx context.Context, port string, v *arptypes.ObjectType) error {
	resp, err := c.svc.WriteSingle(ctx, &gdspb.IDataAccessServiceWriteSingleRequest{
		Data: &gdspb.WriteItem{PortName: port, Value: v},
	})
	if err != nil {
		return fmt.Errorf("plcnext: write %s: %w", port, err)
	}
	if resp.GetXReturnValue() != gdspb.DataAccessError_DAE_None {
		return PortError{Port: port, Code: resp.GetXReturnValue().String()}
	}
	return nil
}

// PortError is the firmware refusing a port: it does not exist (the bus is
// not configured, or the TIC names it differently), or it is not writable.
type PortError struct {
	Port string
	Code string
}

func (e PortError) Error() string { return fmt.Sprintf("plcnext: %s: %s", e.Port, e.Code) }

// ReadBytes reads an OctetstringN port.
func (c *Client) ReadBytes(ctx context.Context, port string) ([]byte, error) {
	v, err := c.read(ctx, port)
	if err != nil {
		return nil, err
	}
	return bytesOf(port, v)
}

func bytesOf(port string, v *arptypes.ObjectType) ([]byte, error) {
	if arr := v.GetArrayValue(); arr != nil {
		out := make([]byte, len(arr.ArrayElements))
		for i, e := range arr.ArrayElements {
			out[i] = byte(e.GetUint8Value())
		}
		return out, nil
	}
	if s, ok := v.GetValue().(*arptypes.ObjectType_StringValue); ok {
		return []byte(s.StringValue), nil
	}
	return nil, fmt.Errorf("plcnext: %s: expected a byte array, got %s", port, v.GetTypeCode())
}

// WriteBytes writes an OctetstringN port whole.
func (c *Client) WriteBytes(ctx context.Context, port string, data []byte) error {
	return c.write(ctx, port, byteArray(data))
}

func byteArray(data []byte) *arptypes.ObjectType {
	elems := make([]*arptypes.ObjectType, len(data))
	for i, b := range data {
		elems[i] = &arptypes.ObjectType{TypeCode: arptypes.CoreType_CT_Uint8, Value: &arptypes.ObjectType_Uint8Value{Uint8Value: uint32(b)}}
	}
	return &arptypes.ObjectType{TypeCode: arptypes.CoreType_CT_Array, Value: &arptypes.ObjectType_ArrayValue{ArrayValue: &arptypes.TypeArray{ArrayElements: elems}}}
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
