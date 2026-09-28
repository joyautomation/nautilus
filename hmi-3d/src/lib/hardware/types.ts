// What every part component receives: the node's props (value, good…) plus
// what <Server> knows from the profile.
import type { NodeProps } from '../defs.js';
import type { Vec3 } from '../scene.js';

export interface PartProps extends NodeProps {
	/** Extents, metres, from the profile. */
	size: Vec3;
	/** The profile binds a tag here (else the position is known-empty or static). */
	bound: boolean;
	/** The profile's stand-in value for a fitted part no driver reports. */
	static?: Record<string, unknown>;
	/** The part library's base URL. */
	models?: string;
	/** Chassis x-ray view is on. */
	xray?: boolean;
}
