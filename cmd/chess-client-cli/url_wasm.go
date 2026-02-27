//go:build js && wasm

package main

import "syscall/js"

// Derive base URL from the page's own origin at runtime
// When served via nginx at domain.com, origin = "https://domain.com"
// and the chess API proxy lives at /chess — so BaseURL = "https://comain.com/chess".
// Works correctly for any deployment domain without rebuilding
var defaultAPIBase = js.Global().Get("location").Get("origin").String() + "/chess"
