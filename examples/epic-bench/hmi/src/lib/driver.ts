// The Driver row's text. With no driver in a frame that has arrived, the
// controller runs the simulated build (sim.yaml, the playground): the memory
// driver is no field driver and reports no status, so the row says so
// rather than showing the same "—" as a screen still waiting for its first
// frame.
export type DriverStatus = { kind: string; state: string };

export function driverLabel(driver: DriverStatus | undefined, frameArrived: boolean): string {
	if (driver) return `${driver.kind} ${driver.state}`;
	return frameArrived ? 'none (simulated)' : '—';
}
