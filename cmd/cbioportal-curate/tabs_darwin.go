//go:build wails && darwin

package main

import (
	"sync"
	"unsafe"

	"github.com/ebitengine/purego/objc"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// Tabs are macOS's own window tabs (as in Safari and Finder): each page is
// a window of its own, shown as a tab of the window that opened it. AppKit
// then gives them the tab bar, Cmd+Shift+[ and ], dragging a tab out to a
// window of its own, and Window > Merge All Windows. Wails knows nothing
// of tabs, so the windows are tabbed here with the Objective-C runtime
// (purego: this builds with and without cgo).

const (
	nsWindowTabbingModePreferred = 1
	nsWindowAbove                = 1
)

var (
	selSetTabbingMode       = objc.RegisterName("setTabbingMode:")
	selSetTabbingIdentifier = objc.RegisterName("setTabbingIdentifier:")
	selAddTabbedWindow      = objc.RegisterName("addTabbedWindow:ordered:")
	selStringWithUTF8String = objc.RegisterName("stringWithUTF8String:")
	selRetain               = objc.RegisterName("retain")
	selSharedApplication    = objc.RegisterName("sharedApplication")
	selKeyWindow            = objc.RegisterName("keyWindow")
	selTabbedWindows        = objc.RegisterName("tabbedWindows")
	selCount                = objc.RegisterName("count")
	selObjectAtIndex        = objc.RegisterName("objectAtIndex:")
	selMakeKeyAndOrderFront = objc.RegisterName("makeKeyAndOrderFront:")
)

// tabbingIdentifier is the NSString all the app's windows share, so they
// tab together.
var tabbingIdentifier = sync.OnceValue(func() objc.ID {
	s := []byte("org.cbioportal.curate.page\x00")
	return objc.ID(objc.GetClass("NSString")).Send(selStringWithUTF8String, unsafe.Pointer(&s[0])).Send(selRetain)
})

// tabbable makes a window take part in tabbing: Cmd+T and the tab bar.
// It must run on the main thread.
func tabbable(w *application.WebviewWindow) objc.ID {
	ns := objc.ID(uintptr(w.NativeWindow()))
	if ns == 0 {
		return 0
	}
	ns.Send(selSetTabbingMode, nsWindowTabbingModePreferred)
	ns.Send(selSetTabbingIdentifier, tabbingIdentifier())
	return ns
}

// addTab shows a new (hidden) window as a tab of parent's window.
// NewWithOptions creates the native window in a later turn of the main
// thread, so this queues after it.
func addTab(parent, tab *application.WebviewWindow) {
	application.InvokeAsync(func() {
		p, t := tabbable(parent), tabbable(tab)
		if p != 0 && t != 0 {
			p.Send(selAddTabbedWindow, t, nsWindowAbove)
		}
		tab.Show()
		tab.Focus()
	})
}

// makeTabbable lets the user tab a window with Cmd+T and Merge All Windows.
func makeTabbable(w *application.WebviewWindow) {
	application.InvokeAsync(func() { tabbable(w) })
}

// selectTab shows the key window's tab n (from 0), or its last tab when n
// is -1, as Cmd+1 to Cmd+9 do in Safari.
func selectTab(n int) {
	application.InvokeAsync(func() {
		key := objc.ID(objc.GetClass("NSApplication")).Send(selSharedApplication).Send(selKeyWindow)
		if key == 0 {
			return
		}
		tabs := key.Send(selTabbedWindows)
		if tabs == 0 {
			return // a window without tabs
		}
		count := int(objc.Send[uint](tabs, selCount))
		if n == -1 {
			n = count - 1
		}
		if n < 0 || n >= count {
			return
		}
		tabs.Send(selObjectAtIndex, uint(n)).Send(selMakeKeyAndOrderFront, objc.ID(0))
	})
}
