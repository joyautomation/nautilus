FUNCTION_BLOCK IoHold
(* Hold outputs after an I/O outage until an operator acknowledges it.

   The driver puts its outputs back on its own when the I/O returns (a
   reconnect, or a watchdog trip, rewrites every command). This block is
   for a program that would rather not drive them again until someone has
   looked: wire it to the driver's status tags and gate the outputs with
   Held. The same pattern as a module-fault bit or an I/O-station-failure
   handler, and nothing vendor-specific — every driver with a status
   delivers the same three tags:

     <id>__Online         BOOL  exchanging data with the I/O now
     <id>__Outages        DINT  times Online went false since start
     <id>__WatchdogTrips  DINT  times the I/O may have driven its outputs
                                to their watchdog (fail-safe) values

   The counters only climb, so a change is never missed between scans.
   Held seals in on any change and releases on a rising edge of Ack while
   Online; an Ack while the I/O is still away does nothing. A count that is
   already above zero on the first scan holds too: on PLCnext, a naut that
   restarts after its outputs were dropped starts with WatchdogTrips = 1.
   Add FIRST_SCAN() to Trip to hold after every start. *)
VAR_INPUT
    Online  : BOOL;
    Outages : DINT;
    Trips   : DINT;
    Ack     : BOOL;   (* operator acknowledge; acts on its rising edge *)
    Trip    : BOOL;   (* anything else that should hold, e.g. FIRST_SCAN() *)
END_VAR
VAR_OUTPUT
    Held : BOOL;
END_VAR
VAR
    seenOutages : DINT;
    seenTrips   : DINT;
    ackEdge     : R_TRIG;
END_VAR

ackEdge(CLK := Ack);
IF ackEdge.Q AND Online THEN
    Held := FALSE;
END_IF;
(* After the release, so an outage in the same scan as an Ack still holds. *)
IF Outages <> seenOutages OR Trips <> seenTrips OR Trip THEN
    Held := TRUE;
END_IF;
seenOutages := Outages;
seenTrips := Trips;
END_FUNCTION_BLOCK
