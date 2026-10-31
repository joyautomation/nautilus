// browse.go walks a server's address space for `naut opcua import` and
// `naut opcua browse`: every Variable node under a root, with the
// attributes a binding needs (DataType, AccessLevel, ValueRank) and the
// ones a tag file wants (DisplayName, Description). Attributes are read in
// batches per level — one Read for every variable a folder holds — so a
// gateway exposing ten thousand tags is browsed in seconds, not minutes.
package opcua

import (
	"context"
	"fmt"
	"strings"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// Variable is one Variable node as browsed.
type Variable struct {
	// NodeID in standard string form, as the server identifies it now.
	NodeID string
	// Namespace is the URI of the node's namespace, from the server's
	// table — what the manifest records so a renumbering is survived.
	Namespace string
	// Path is the browse-name path below the root, root excluded.
	Path []string
	// DisplayName and Description as the server shows them.
	DisplayName string
	Description string
	// Type is the binding type name when the Value is a scalar built-in;
	// otherwise the DataType node id as a string (the generator skips it
	// and says why).
	Type string
	// Scalar reports a built-in scalar type (Type is then a TypeNames entry).
	Scalar bool
	// ValueRank: -1 scalar, ≥ 0 or -3/-2 may be an array.
	ValueRank int32
	// Readable/Writable come from AccessLevel (CurrentRead/CurrentWrite).
	Readable bool
	Writable bool
}

// BrowseResult is one walk's findings.
type BrowseResult struct {
	Root       string
	Namespaces []string
	Variables  []Variable
	// Visited counts the nodes browsed, for the progress line.
	Visited int
}

// ObjectsFolder is the default root: everything a server exposes hangs
// under Objects (i=85).
var ObjectsFolder = ua.NewNumericNodeID(0, id.ObjectsFolder)

// Browse walks the hierarchy under root to maxDepth levels (0 = unlimited)
// and returns every Variable found. progress, when set, is called with a
// one-line status every few hundred nodes. The Server object (i=2253) is
// skipped unless it IS the root: its diagnostics tree is large, churns,
// and is never what a project wants to poll.
func Browse(ctx context.Context, c *opcua.Client, root *ua.NodeID, maxDepth int, progress func(string)) (BrowseResult, error) {
	res := BrowseResult{Root: root.String(), Namespaces: c.Namespaces()}
	visited := map[string]bool{}
	serverObj := ua.NewNumericNodeID(0, id.Server).String()

	var walk func(node *ua.NodeID, path []string, depth int) error
	walk = func(node *ua.NodeID, path []string, depth int) error {
		key := node.String()
		if visited[key] {
			return nil
		}
		visited[key] = true
		res.Visited++
		if progress != nil && res.Visited%200 == 0 {
			progress(fmt.Sprintf("browsed %d nodes, %d variables so far (%s)", res.Visited, len(res.Variables), strings.Join(path, "/")))
		}
		refs, err := browseChildren(ctx, c, node)
		if err != nil {
			return fmt.Errorf("browse %s: %w", key, err)
		}

		// Variables at this level, attributes in one batched Read.
		var vars []*ua.ReferenceDescription
		for _, r := range refs {
			if r.NodeClass == ua.NodeClassVariable {
				vars = append(vars, r)
			}
		}
		if len(vars) > 0 {
			found, err := readVariables(ctx, c, vars, path, res.Namespaces)
			if err != nil {
				return err
			}
			res.Variables = append(res.Variables, found...)
		}
		if maxDepth > 0 && depth >= maxDepth {
			return nil
		}
		for _, r := range refs {
			if r.NodeClass != ua.NodeClassObject && r.NodeClass != ua.NodeClassVariable {
				continue
			}
			child := r.NodeID.NodeID
			if child.String() == serverObj && depth > 0 || (child.String() == serverObj && root.String() != serverObj) {
				continue
			}
			if err := walk(child, append(append([]string(nil), path...), browseName(r)), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, nil, 0); err != nil {
		return res, err
	}
	return res, nil
}

// browseChildren returns the forward hierarchical references of one node,
// following continuation points.
func browseChildren(ctx context.Context, c *opcua.Client, node *ua.NodeID) ([]*ua.ReferenceDescription, error) {
	resp, err := c.Browse(ctx, &ua.BrowseRequest{
		RequestedMaxReferencesPerNode: 0,
		NodesToBrowse: []*ua.BrowseDescription{{
			NodeID:          node,
			BrowseDirection: ua.BrowseDirectionForward,
			ReferenceTypeID: ua.NewNumericNodeID(0, id.HierarchicalReferences),
			IncludeSubtypes: true,
			NodeClassMask:   uint32(ua.NodeClassObject | ua.NodeClassVariable),
			ResultMask:      uint32(ua.BrowseResultMaskAll),
		}},
	})
	if err != nil {
		return nil, err
	}
	if len(resp.Results) == 0 {
		return nil, nil
	}
	r := resp.Results[0]
	if !good(r.StatusCode) {
		return nil, r.StatusCode
	}
	refs := r.References
	cp := r.ContinuationPoint
	for len(cp) > 0 {
		next, err := c.BrowseNext(ctx, &ua.BrowseNextRequest{ContinuationPoints: [][]byte{cp}})
		if err != nil {
			return nil, err
		}
		if len(next.Results) == 0 {
			break
		}
		refs = append(refs, next.Results[0].References...)
		cp = next.Results[0].ContinuationPoint
	}
	return refs, nil
}

// readVariables reads DataType, ValueRank, AccessLevel and Description for
// a level's variables in one request (chunked to the server's limit).
func readVariables(ctx context.Context, c *opcua.Client, refs []*ua.ReferenceDescription, path []string, nss []string) ([]Variable, error) {
	attrs := []ua.AttributeID{ua.AttributeIDDataType, ua.AttributeIDValueRank, ua.AttributeIDAccessLevel, ua.AttributeIDDescription}
	const perReq = 100 // nodes per Read; ×4 attributes stays under every server's limit
	var out []Variable
	for start := 0; start < len(refs); start += perReq {
		end := min(start+perReq, len(refs))
		chunk := refs[start:end]
		ids := make([]*ua.ReadValueID, 0, len(chunk)*len(attrs))
		for _, r := range chunk {
			for _, a := range attrs {
				ids = append(ids, &ua.ReadValueID{NodeID: r.NodeID.NodeID, AttributeID: a})
			}
		}
		resp, err := c.Read(ctx, &ua.ReadRequest{NodesToRead: ids})
		if err != nil {
			return nil, fmt.Errorf("read attributes: %w", err)
		}
		if len(resp.Results) != len(ids) {
			return nil, fmt.Errorf("read attributes: %d results for %d nodes", len(resp.Results), len(ids))
		}
		for i, r := range chunk {
			dv := resp.Results[i*len(attrs) : (i+1)*len(attrs)]
			v := Variable{
				NodeID:      r.NodeID.NodeID.String(),
				Path:        append(append([]string(nil), path...), browseName(r)),
				DisplayName: displayName(r),
				ValueRank:   -1,
				Readable:    true,
			}
			if ns := int(r.NodeID.NodeID.Namespace()); ns < len(nss) {
				v.Namespace = nss[ns]
			}
			// DataType is a NodeId on the wire; some servers (gopcua's
			// own) hand back an ExpandedNodeId.
			var dt *ua.NodeID
			switch x := value(dv[0]).(type) {
			case *ua.NodeID:
				dt = x
			case *ua.ExpandedNodeID:
				if x != nil {
					dt = x.NodeID
				}
			}
			if dt != nil {
				if name, scalar := TypeName(dt); scalar {
					v.Type, v.Scalar = name, true
				} else {
					v.Type = dt.String()
				}
			}
			if rank, ok := value(dv[1]).(int32); ok {
				v.ValueRank = rank
			}
			if al, ok := value(dv[2]).(uint8); ok {
				v.Readable = al&uint8(ua.AccessLevelTypeCurrentRead) != 0
				v.Writable = al&uint8(ua.AccessLevelTypeCurrentWrite) != 0
			}
			if lt, ok := value(dv[3]).(*ua.LocalizedText); ok && lt != nil {
				v.Description = lt.Text
			}
			out = append(out, v)
		}
	}
	return out, nil
}

// value unwraps a good DataValue's Variant, or nil.
func value(dv *ua.DataValue) any {
	if dv == nil || !good(dv.Status) || dv.Value == nil {
		return nil
	}
	return dv.Value.Value()
}

func browseName(r *ua.ReferenceDescription) string {
	if r.BrowseName != nil && r.BrowseName.Name != "" {
		return r.BrowseName.Name
	}
	return displayName(r)
}

func displayName(r *ua.ReferenceDescription) string {
	if r.DisplayName != nil && r.DisplayName.Text != "" {
		return r.DisplayName.Text
	}
	if r.BrowseName != nil {
		return r.BrowseName.Name
	}
	return r.NodeID.NodeID.String()
}
