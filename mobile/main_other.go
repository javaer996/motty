//go:build !ios && !android

// Package main is empty on desktop platforms so `go build ./...` keeps
// working; the C ABI exists only in the iOS archive (see main_ios.go) and the
// Android shared library (see main_android.go).
package main

func main() {}
