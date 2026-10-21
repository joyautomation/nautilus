package main

import (
"context"
"fmt"
"log"
"net"
"strconv"
"time"

opcserver "github.com/gopcua/opcua/server"
"github.com/gopcua/opcua/ua"
"github.com/joyautomation/nautilus/internal/project"
"github.com/joyautomation/nautilus/runtime"
)

func startOPCUAServer(ctx context.Context, rt *runtime.Runtime, cfg *project.OPCUAConfig) {
endpoint := "localhost:4840"
interval := time.Second

if cfg != nil {
if cfg.Endpoint != "" {
endpoint = cfg.Endpoint
}
if cfg.Interval != "" {
d, err := time.ParseDuration(cfg.Interval)
if err != nil || d <= 0 {
log.Printf("OPC UA: invalid interval %q", cfg.Interval)
return
}
interval = d
}
}

host, portText, err := net.SplitHostPort(endpoint)
if err != nil {
log.Printf("OPC UA: invalid endpoint %q: %v", endpoint, err)
return
}
port, err := strconv.Atoi(portText)
if err != nil || port < 1 || port > 65535 {
log.Printf("OPC UA: invalid port in endpoint %q", endpoint)
return
}

srv := opcserver.New(
opcserver.EndPoint(host, port),
opcserver.ServerName("Nautilus OPC UA Server"),
opcserver.ProductName("Nautilus"),
opcserver.EnableSecurity("None", ua.MessageSecurityModeNone),
opcserver.EnableAuthMode(ua.UserTokenTypeAnonymous),
)

ns := opcserver.NewMapNamespace(srv, "urn:joyautomation:nautilus")

// Create one OPC UA variable for every runtime tag.
values := rt.Tags().All()
ns.Mu.Lock()
for name, value := range values {
ns.Data[name] = opcuaValue(value)
}
ns.Mu.Unlock()

rootNS, err := srv.Namespace(0)
if err != nil {
log.Printf("OPC UA: cannot get namespace 0: %v", err)
return
}

objectsNode := rootNS.Node(ua.NewNumericNodeID(0, 85))
if objectsNode == nil {
log.Println("OPC UA: Objects node not found")
return
}
objectsNode.AddRef(ns.Objects(), opcserver.RefTypeIDOrganizes, true)

if err := srv.Start(ctx); err != nil {
log.Printf("OPC UA: server start failed: %v", err)
return
}
defer srv.Close()

log.Printf("OPC UA: listening on opc.tcp://%s; namespace ID=%d; tags=%d",
net.JoinHostPort(host, portText), ns.ID(), len(values))

ticker := time.NewTicker(interval)
defer ticker.Stop()

for {
select {
case <-ctx.Done():
log.Println("OPC UA: server stopping")
return
case <-ticker.C:
values := rt.Tags().All()
for name, value := range values {
ns.SetValue(name, opcuaValue(value))
}
}
}
}

// Keep tag values in OPC UA-compatible primitive forms.
func opcuaValue(value any) any {
switch v := value.(type) {
case nil, bool, string,
int, int8, int16, int32, int64,
uint, uint8, uint16, uint32, uint64,
float64:
return v
case float32:
return float64(v)
case time.Time:
return v.UTC().Format(time.RFC3339Nano)
default:
return fmt.Sprint(v)
}
}
