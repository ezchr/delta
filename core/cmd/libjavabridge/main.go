// Command libjavabridge is the native library the Endstone plugin loads
// (go build -buildmode=c-shared). It exports a small C interface; see docs/NATIVE.md.
//
// The library is loaded once per process and never unloaded (a Go runtime cannot be), so
// jb_start/jb_stop may be called any number of times in turn (plugin reloads).
package main

/*
#include <stdlib.h>
#include <stdint.h>
*/
import "C"

import (
	"time"
	"unsafe"

	"javabridge/core/host"
	_ "javabridge/core/service"
)

// version is set with -ldflags "-X main.version=...".
var version = "dev"

// jb_version returns the library version. Free the result with jb_free.
//
//export jb_version
func jb_version() *C.char { return C.CString(version) }

// jb_start starts the core with a JSON configuration. Returns 0 on success, -1 on error
// (jb_last_error says why), -2 if it is already running.
//
//export jb_start
func jb_start(cfg *C.char) C.int {
	err := host.Start([]byte(C.GoString(cfg)))
	switch {
	case err == nil:
		return 0
	case err == host.ErrRunning:
		return -2
	default:
		return -1
	}
}

// jb_stop stops everything jb_start started, waiting at most timeout_ms. Returns 0, or -1 when
// something did not stop in time or failed (jb_last_error says what).
//
//export jb_stop
func jb_stop(timeoutMS C.int) C.int {
	if err := host.Stop(time.Duration(timeoutMS) * time.Millisecond); err != nil {
		return -1
	}
	return 0
}

// jb_running returns 1 while the core runs.
//
//export jb_running
func jb_running() C.int {
	if host.Running() {
		return 1
	}
	return 0
}

// jb_poll_log returns the queued log lines ("LEVEL\tmessage\n" each) or NULL when there are
// none. Free the result with jb_free.
//
//export jb_poll_log
func jb_poll_log() *C.char {
	s := host.DrainLogs()
	if s == "" {
		return nil
	}
	return C.CString(s)
}

// jb_call sends a JSON request {"method":..., "params":...} to the running core and returns its
// JSON answer {"ok":bool, "result"|"error":...}. Free the result with jb_free.
//
//export jb_call
func jb_call(req *C.char) *C.char {
	return C.CString(string(host.Call([]byte(C.GoString(req)))))
}

// jb_last_error returns the last jb_start/jb_stop error ("" if none) and clears it. Free the
// result with jb_free.
//
//export jb_last_error
func jb_last_error() *C.char { return C.CString(host.LastError()) }

// jb_free frees a string this library returned.
//
//export jb_free
func jb_free(p *C.char) { C.free(unsafe.Pointer(p)) }

func main() {}
