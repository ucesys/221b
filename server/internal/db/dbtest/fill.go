package dbtest

import (
	"encoding/json"
	"reflect"
	"strconv"
	"time"
)

// Fill sets every field of the struct v points to, and of everything in
// it, to a value of its own: strings and numbers that differ from field
// to field, true, times a second apart, two elements in every list and
// map, every pointer set. A test that saves a filled record and reads it
// back equal has shown that no field is dropped. Raw JSON is left empty,
// and a type that holds itself (a hand-over's own hand-overs) is filled a
// few levels deep.
func Fill(v any) {
	n := 0
	fill(reflect.ValueOf(v).Elem(), &n, 0)
}

// maxDepth is how deep lists, maps and pointers are followed.
const maxDepth = 7

var (
	timeType = reflect.TypeOf(time.Time{})
	rawType  = reflect.TypeOf(json.RawMessage{})
)

func fill(v reflect.Value, n *int, depth int) {
	if !v.CanSet() || depth > maxDepth {
		return
	}
	*n++
	switch {
	case v.Type() == timeType:
		v.Set(reflect.ValueOf(time.Date(2026, 11, 19, 0, 0, *n, 0, time.UTC)))
		return
	case v.Type() == rawType:
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString("v" + strconv.Itoa(*n))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(*n))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(*n))
	case reflect.Float32, reflect.Float64:
		v.SetFloat(float64(*n) + 0.5)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), n, depth+1)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			fill(v.Field(i), n, depth)
		}
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 2, 2)
		for i := 0; i < 2; i++ {
			fill(s.Index(i), n, depth+1)
		}
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		for i := 0; i < 2; i++ {
			k, e := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
			fill(k, n, depth+1)
			fill(e, n, depth+1)
			m.SetMapIndex(k, e)
		}
		v.Set(m)
	}
}
