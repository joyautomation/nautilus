//go:build !js

package project

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/joyautomation/nautilus/devbind"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/server"
)

// ParamHook adapts the project's IO-Link port devices to the server's
// device parameter routes (server.ParamHook, #705): each request finds the
// master driver that has the port device among the runtime's live drivers
// (a live I/O swap replaces a master's driver, so they are asked each
// time) and goes through its ISDURead and ISDUWrite. nil when the project
// has no port devices.
func (p *Project) ParamHook() *server.ParamHook {
	if len(p.devices.Ports()) == 0 {
		return nil
	}
	return paramHook(func() []nio.NamedDriver {
		if m, ok := p.Runtime.Driver.(*nio.Multi); ok {
			return m.Children()
		}
		return p.drivers
	})
}

// paramHook is ParamHook over a source of the live drivers.
func paramHook(live func() []nio.NamedDriver) *server.ParamHook {
	find := func(port string) *devbind.OPCUADriver {
		for _, nd := range live() {
			if d := opcuaMaster(nd.Driver); d != nil {
				if _, _, ok := d.ISDUParams(port); ok {
					return d
				}
			}
		}
		return nil
	}
	missing := func(port string) error {
		return fmt.Errorf("%w: no port device %q", server.ErrParamUnknown, port)
	}
	return &server.ParamHook{
		List: func(port string) (server.ParamList, bool) {
			d := find(port)
			if d == nil {
				return server.ParamList{}, false
			}
			params, master, _ := d.ISDUParams(port)
			out := server.ParamList{Port: port, Master: master}
			for _, q := range params {
				out.Params = append(out.Params, server.ParamInfo{ID: q.ID, Name: q.Name, Index: q.Index, Ref: q.Ref, Type: q.Type,
					Access: q.Access, Octets: q.Octets, Range: q.Range, Default: q.Default, Carried: q.Carried})
			}
			return out, true
		},
		Read: func(ctx context.Context, port, ref string) (server.ParamAnswer, error) {
			d := find(port)
			if d == nil {
				return server.ParamAnswer{}, missing(port)
			}
			res, err := d.ISDURead(ctx, port, ref)
			return paramAnswer(port, res), paramError(err)
		},
		Write: func(ctx context.Context, port, ref string, v any) (server.ParamAnswer, error) {
			d := find(port)
			if d == nil {
				return server.ParamAnswer{}, missing(port)
			}
			res, err := d.ISDUWrite(ctx, port, ref, v)
			return paramAnswer(port, res), paramError(err)
		},
	}
}

// opcuaMaster is the OPC UA master adapter d is or wraps.
func opcuaMaster(d nio.Driver) *devbind.OPCUADriver {
	for d != nil {
		if m, ok := d.(*devbind.OPCUADriver); ok {
			return m
		}
		u, ok := d.(interface{ Unwrap() nio.Driver })
		if !ok {
			return nil
		}
		d = u.Unwrap()
	}
	return nil
}

func paramAnswer(port string, r devbind.ISDUResult) server.ParamAnswer {
	v := r.Value
	if b, ok := v.([]byte); ok {
		v = hex.EncodeToString(b) // an octet string is shown as hex text
	}
	return server.ParamAnswer{Port: port, Variable: r.Variable, Index: r.Index, Subindex: r.Subindex, Value: v,
		Raw: hex.EncodeToString(r.Raw), Identity: r.Identity.String(), IdentityChecked: r.Checked}
}

// paramError wraps devbind's kinds of refusal in the server's, so each is
// answered with its own status; the master's own failures pass as they are.
func paramError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, devbind.ErrISDUUnknown):
		return fmt.Errorf("%w: %w", server.ErrParamUnknown, err)
	case errors.Is(err, devbind.ErrISDUInvalid):
		return fmt.Errorf("%w: %w", server.ErrParamInvalid, err)
	case errors.Is(err, devbind.ErrISDUUncertain):
		return fmt.Errorf("%w: %w", server.ErrParamUncertain, err)
	case errors.Is(err, devbind.ErrISDURefused):
		return fmt.Errorf("%w: %w", server.ErrParamRefused, err)
	}
	return err
}
