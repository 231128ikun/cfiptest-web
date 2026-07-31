//go:build !windows

package main

func installConsoleCloseHandler(_ chan<- struct{}, _ <-chan struct{}) func() {
	return func() {}
}
