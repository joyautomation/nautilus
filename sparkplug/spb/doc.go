// Package spb holds the generated Sparkplug B protobuf types — the Eclipse
// Tahu schema (org.eclipse.tahu.protobuf), byte-for-byte the same .proto the
// joyautomation/sparkplug-tck-go conformance kit uses, so our payloads decode
// identically under test.
//
// sparkplug_b.pb.go is generated and committed (so building nautilus needs no
// protoc). To regenerate after editing proto/sparkplug_b.proto:
//
//	protoc --proto_path=sparkplug/spb/proto --go_out=. \
//	  --go_opt=module=github.com/joyautomation/nautilus \
//	  --go_opt=Msparkplug_b.proto=github.com/joyautomation/nautilus/sparkplug/spb \
//	  sparkplug/spb/proto/sparkplug_b.proto
//
// Requires protoc + protoc-gen-go v1.36.x (matching google.golang.org/protobuf).
package spb
