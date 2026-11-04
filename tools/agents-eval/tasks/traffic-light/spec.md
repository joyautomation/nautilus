Program a traffic light for a four-way intersection in ladder logic (LD),
in a new file `traffic.ld`, running as its own task every 100 ms. (The class
has not covered SFC yet, so ladder please.)

Tags (add them to nautilus.yaml with exactly these names):

- `Enable` (input, BOOL): the controller's run switch.
- `NS_Red`, `NS_Yellow`, `NS_Green` (output, BOOL): north-south lamps.
- `EW_Red`, `EW_Yellow`, `EW_Green` (output, BOOL): east-west lamps.

How it should work:

1. While `Enable` is on, the lights run this cycle and repeat it:
   north-south green 20 s, north-south yellow 3 s, all red 2 s,
   east-west green 20 s, east-west yellow 3 s, all red 2 s.
   The direction that is not green or yellow shows red.
2. Each direction shows exactly one lamp at any moment, and the two
   directions are never green or yellow at the same time.
3. While `Enable` is off, both directions show red only. Turning it on
   starts the cycle from the beginning (north-south green).
