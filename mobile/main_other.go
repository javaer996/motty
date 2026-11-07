//go:build !ios

// Package main is empty on desktop platforms so `go build ./...` keeps
// working; the C ABI exists only in the iOS archive (see main_ios.go).
package main

func main() {}
