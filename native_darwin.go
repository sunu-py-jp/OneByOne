//go:build darwin && desktop

package main

// #cgo LDFLAGS: -framework UniformTypeIdentifiers
import "C"

// Wails' native file dialog uses UTType on current macOS SDKs.
