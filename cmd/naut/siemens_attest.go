package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// deviceAttestUsage is the help for --attest-order and --attest-firmware,
// shared by every naut siemens command that goes online.
const deviceAttestUsage = `  --attest-order ORDER --attest-firmware VERSION
                the CPU's order number and firmware as you read them off
                the CPU itself (its label or display). Only for a CPU the
                agent holds a device pin for and cannot identify online
                (it cannot yet, #789): the agent compares them with the
                pin and refuses any difference; the attestation is audited
                and a download's record keeps it. A pin is made only by an
                upload (--adopt upload) or by hand, never by these
`

// deviceAttestFlags are --attest-order and --attest-firmware (#1325).
type deviceAttestFlags struct {
	order, firmware string
}

func (f *deviceAttestFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.order, "attest-order", "", "the CPU's order number, as you read it off the CPU itself")
	fs.StringVar(&f.firmware, "attest-firmware", "", "the CPU's firmware version, as you read it off the CPU itself")
}

// attestation is the operator's device attestation, nil without the
// flags. by is who gives it (the local user).
func (f deviceAttestFlags) attestation(by string) (*tiad.DeviceAttestation, error) {
	if f.order == "" && f.firmware == "" {
		return nil, nil
	}
	if strings.TrimSpace(f.order) == "" || strings.TrimSpace(f.firmware) == "" {
		return nil, errors.New("--attest-order and --attest-firmware go together: the order number and the firmware, both as you read them off the CPU")
	}
	if strings.TrimSpace(by) == "" {
		return nil, errors.New("the operator's name (the local user) could not be read, and a device attestation must name who gives it")
	}
	a := &tiad.DeviceAttestation{By: by, Order: f.order, Firmware: f.firmware}
	if err := a.Validate(); err != nil {
		return nil, fmt.Errorf("--attest-order/--attest-firmware: %v", err)
	}
	return a, nil
}

// describeDeviceAttestation is an accepted attestation in words.
func describeDeviceAttestation(r *tiad.DeviceAttestationRecord) string {
	return fmt.Sprintf("checked against your attestation, not read online (#789): %s read order %q, firmware %q off the CPU; the pin holds order %q, firmware %q",
		r.By, r.Order, r.Firmware, r.Pinned.Order, r.Pinned.Firmware)
}
