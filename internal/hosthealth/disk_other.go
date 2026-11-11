//go:build !linux

package hosthealth

func platformProbe(string) (func() probeResult, func() kernelSample) { return nil, nil }
