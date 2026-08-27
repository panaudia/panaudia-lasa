package main

// darwin reports back exactly what was set, and kern.ipc.maxsockbuf
// (8 MiB stock) already allows it; no capability model to drive.
const sufficientUDPBuffer = desiredUDPBuffer

func udpPrelude(appConfig) {}
