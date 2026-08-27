//go:build !linux && !darwin

package main

import "net"

const sufficientUDPBuffer = desiredUDPBuffer

func udpPrelude(appConfig)                   {}
func readUDPBuffers(*net.UDPConn) udpBuffers { return udpBuffers{} }
