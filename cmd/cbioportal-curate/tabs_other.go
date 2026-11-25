//go:build wails && !darwin

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// Window tabs are macOS's; elsewhere a page opens in a window of its own.

func addTab(parent, tab *application.WebviewWindow) {
	tab.Show()
	tab.Focus()
}

func makeTabbable(*application.WebviewWindow) {}

func selectTab(int) {}
