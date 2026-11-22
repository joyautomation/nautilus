package project

import (
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/checkout"
	"github.com/joyautomation/nautilus/runtime"
)

// CheckCheckout is what `naut check` says about a checkout: every point
// names a tag the project has, a point that is forced is an output the
// controller drives, and a single-person sign-off is a warning.
func (p *Project) CheckCheckout(rt *runtime.Runtime) (errs, warns []string) {
	if p.checkout == nil {
		return nil, nil
	}
	tags := rt.Tags()
	for _, pt := range p.checkout.Points {
		root := pt.Tag
		if i := strings.IndexAny(root, ".["); i > 0 {
			root = root[:i]
		}
		if _, ok := tags.ReadPath(pt.Tag); !ok {
			errs = append(errs, fmt.Sprintf("checkout point %q: the project has no tag %q", pt.ID, pt.Tag))
			continue
		}
		if _, out := tags.DeclaredOutput(root); !out {
			switch {
			case pt.Method == checkout.Force:
				errs = append(errs, fmt.Sprintf("checkout point %q: %q is not an output the controller drives, so a force would not reach the field: observe it, or declare the tag role: output", pt.ID, pt.Tag))
			case checkout.Output(pt.Kind):
				warns = append(warns, fmt.Sprintf("checkout point %q is a %s but %q is not a declared output", pt.ID, pt.Kind, pt.Tag))
			}
		}
	}
	if p.Checkout.SignoffRule() == SignoffSingle {
		warns = append(warns, "checkout.signoff: single: the person who recorded a checkout's results may also seal it; the sealed record says single-person")
	}
	return errs, warns
}
