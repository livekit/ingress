// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gstutil

// #cgo pkg-config: gobject-2.0
// #include <stdlib.h>
// #include <glib-object.h>
//
// typedef enum {
//   PROPERTY_OK,
//   PROPERTY_MISSING,
//   PROPERTY_NOT_WRITABLE,
//   PROPERTY_CONSTRUCT_ONLY,
//   PROPERTY_WRONG_TYPE,
//   PROPERTY_INVALID,
// } property_check;
//
// // The checks g_object_set_property makes before setting a property.
// static property_check check_property(GObject *obj, const gchar *name, const GValue *value, GType *type) {
//   GParamSpec *pspec = g_object_class_find_property(G_OBJECT_GET_CLASS(obj), name);
//   if (pspec == NULL)
//     return PROPERTY_MISSING;
//   *type = pspec->value_type;
//   if (!(pspec->flags & G_PARAM_WRITABLE))
//     return PROPERTY_NOT_WRITABLE;
//   if (pspec->flags & G_PARAM_CONSTRUCT_ONLY)
//     return PROPERTY_CONSTRUCT_ONLY;
//
//   property_check ret = PROPERTY_OK;
//   GValue tmp = G_VALUE_INIT;
//   g_value_init(&tmp, pspec->value_type);
//   if (!g_value_transform(value, &tmp))
//     ret = PROPERTY_WRONG_TYPE;
//   else if (g_param_value_validate(pspec, &tmp) && !(pspec->flags & G_PARAM_LAX_VALIDATION))
//     ret = PROPERTY_INVALID;
//   g_value_unset(&tmp);
//   return ret;
// }
import "C"

import (
	"runtime"
	"unsafe"

	"github.com/go-gst/go-glib/pkg/gobject/v2"
	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/livekit/psrpc"
)

// SetProperty sets a property on an element, returning an error in the cases where
// SetObjectProperty would only log a GLib critical.
func SetProperty(elem gst.Element, name string, value any) error {
	v, err := newValue(value)
	if err != nil {
		return psrpc.NewErrorf(psrpc.Internal, "could not set %s on %s: %v", name, elem.GetName(), err)
	}

	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	obj := (*C.GObject)(gst.UnsafeElementToGlibNone(elem))
	cvalue := (*C.GValue)(gobject.UnsafeValueToGlibNone(v))

	var want C.GType
	check := C.check_property(obj, cname, cvalue, &want)
	if check == C.PROPERTY_OK {
		C.g_object_set_property(obj, cname, cvalue)
	}
	runtime.KeepAlive(elem)
	runtime.KeepAlive(v)

	switch check {
	case C.PROPERTY_OK:
		return nil
	case C.PROPERTY_MISSING:
		return psrpc.NewErrorf(psrpc.Internal, "%s has no property %s", elem.GetName(), name)
	case C.PROPERTY_NOT_WRITABLE:
		return psrpc.NewErrorf(psrpc.Internal, "property %s of %s is not writable", name, elem.GetName())
	case C.PROPERTY_CONSTRUCT_ONLY:
		return psrpc.NewErrorf(psrpc.Internal, "property %s of %s can only be set at construction", name, elem.GetName())
	case C.PROPERTY_WRONG_TYPE:
		return psrpc.NewErrorf(psrpc.Internal, "property %s of %s takes %s, not %s", name, elem.GetName(), gobject.Type(want).Name(), v.Type().Name())
	default:
		return psrpc.NewErrorf(psrpc.Internal, "value %v is out of range for property %s of %s", value, name, elem.GetName())
	}
}

// newValue converts value to a GValue, returning the panic gobject.NewValue raises for
// unsupported types such as int and uint as an error.
func newValue(value any) (v *gobject.Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = psrpc.NewErrorf(psrpc.Internal, "%v", r)
		}
	}()
	return gobject.NewValue(value), nil
}
