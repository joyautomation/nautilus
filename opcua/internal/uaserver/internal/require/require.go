// Package require is the handful of github.com/stretchr/testify/require
// assertions the tests copied from gopcua use, with the same names and
// argument order, so those tests run unchanged without adding testify to
// this module. Each one stops the test on failure, as testify's does.
package require

import (
	"bytes"
	"cmp"
	"fmt"
	"reflect"
	"testing"
)

func fail(t testing.TB, what string, msgAndArgs []any) {
	t.Helper()
	if len(msgAndArgs) > 0 {
		if f, ok := msgAndArgs[0].(string); ok {
			what += ": " + fmt.Sprintf(f, msgAndArgs[1:]...)
		} else {
			what += ": " + fmt.Sprint(msgAndArgs...)
		}
	}
	t.Fatal(what)
}

// equal is testify's ObjectsAreEqual: byte slices by content, the rest deeply.
func equal(want, got any) bool {
	wb, wok := want.([]byte)
	gb, gok := got.([]byte)
	if wok && gok {
		return bytes.Equal(wb, gb)
	}
	return reflect.DeepEqual(want, got)
}

func isEmpty(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Array, reflect.Chan, reflect.Map, reflect.Slice, reflect.String:
		return rv.Len() == 0
	case reflect.Pointer:
		if rv.IsNil() {
			return true
		}
		return isEmpty(rv.Elem().Interface())
	}
	return rv.IsZero()
}

func Equal(t testing.TB, want, got any, msgAndArgs ...any) {
	t.Helper()
	if !equal(want, got) {
		fail(t, fmt.Sprintf("not equal:\nwant %#v\n got %#v", want, got), msgAndArgs)
	}
}

func NoError(t testing.TB, err error, msgAndArgs ...any) {
	t.Helper()
	if err != nil {
		fail(t, fmt.Sprintf("unexpected error: %v", err), msgAndArgs)
	}
}

func True(t testing.TB, ok bool, msgAndArgs ...any) {
	t.Helper()
	if !ok {
		fail(t, "should be true", msgAndArgs)
	}
}

func NotNil(t testing.TB, v any, msgAndArgs ...any) {
	t.Helper()
	if v == nil || (reflect.ValueOf(v).Kind() == reflect.Pointer && reflect.ValueOf(v).IsNil()) {
		fail(t, "should not be nil", msgAndArgs)
	}
}

func Empty(t testing.TB, v any, msgAndArgs ...any) {
	t.Helper()
	if !isEmpty(v) {
		fail(t, fmt.Sprintf("should be empty: %#v", v), msgAndArgs)
	}
}

func NotEmpty(t testing.TB, v any, msgAndArgs ...any) {
	t.Helper()
	if isEmpty(v) {
		fail(t, "should not be empty", msgAndArgs)
	}
}

func Len(t testing.TB, v any, n int, msgAndArgs ...any) {
	t.Helper()
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Array, reflect.Chan, reflect.Map, reflect.Slice, reflect.String:
		if rv.Len() != n {
			fail(t, fmt.Sprintf("has %d item(s), want %d", rv.Len(), n), msgAndArgs)
		}
	default:
		fail(t, fmt.Sprintf("%T has no length", v), msgAndArgs)
	}
}

func Greater[T cmp.Ordered](t testing.TB, a, b T, msgAndArgs ...any) {
	t.Helper()
	if !(a > b) {
		fail(t, fmt.Sprintf("%v is not greater than %v", a, b), msgAndArgs)
	}
}

func GreaterOrEqual[T cmp.Ordered](t testing.TB, a, b T, msgAndArgs ...any) {
	t.Helper()
	if !(a >= b) {
		fail(t, fmt.Sprintf("%v is not greater than or equal to %v", a, b), msgAndArgs)
	}
}

func Less[T cmp.Ordered](t testing.TB, a, b T, msgAndArgs ...any) {
	t.Helper()
	if !(a < b) {
		fail(t, fmt.Sprintf("%v is not less than %v", a, b), msgAndArgs)
	}
}

func LessOrEqual[T cmp.Ordered](t testing.TB, a, b T, msgAndArgs ...any) {
	t.Helper()
	if !(a <= b) {
		fail(t, fmt.Sprintf("%v is not less than or equal to %v", a, b), msgAndArgs)
	}
}
