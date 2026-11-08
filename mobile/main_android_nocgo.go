//go:build android && !cgo

// Package main is empty when Android is built without cgo, so
// `GOOS=android go build ./mobile` keeps working; the shared library only
// exists in the cgo build (see main_android.go).
package main

func main() {}
