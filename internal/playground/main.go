// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build js && wasm

// Command playground is the WebAssembly side of the documentation site's
// query playground. It exposes one function to the page:
//
//	quarkRender(specJSON string) string
//
// The argument is a render.Spec encoded as JSON; the return value is a
// render.Result encoded as JSON. Nothing is executed against any database:
// the six dialects render their SQL through a driver that records the
// statement and answers an empty result.
//
// Build it with scripts/ci/build_playground.sh <out-dir>, which also copies
// the wasm_exec.js shim the page loads before it.
package main

import (
	"syscall/js"

	"github.com/jcsvwinston/quark/internal/playground/render"
)

func main() {
	r, err := render.New()
	if err != nil {
		js.Global().Get("console").Call("error", "quark playground: "+err.Error())
		return
	}
	js.Global().Set("quarkRender", js.FuncOf(func(_ js.Value, args []js.Value) any {
		spec := ""
		if len(args) > 0 && args[0].Type() == js.TypeString {
			spec = args[0].String()
		}
		return r.RenderJSON(spec)
	}))
	// Keep the Go runtime alive: the function above is called from JS for
	// as long as the page lives.
	select {}
}
