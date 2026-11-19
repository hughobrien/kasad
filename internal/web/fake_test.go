package web

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/hughobrien/kasad/internal/exporter"
)

// fakeDevices stands in for the Exporter.
type fakeDevices struct {
	mu           sync.Mutex
	devs         []exporter.DeviceView
	sets         []string // "host=true"
	setErr       error
	subs         map[chan struct{}]bool
	unsubscribed atomic.Int32
}

func newFake(devs ...exporter.DeviceView) *fakeDevices {
	return &fakeDevices{devs: devs, subs: map[chan struct{}]bool{}}
}

func (f *fakeDevices) Devices() []exporter.DeviceView {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]exporter.DeviceView(nil), f.devs...)
}

func (f *fakeDevices) SetRelay(_ context.Context, host string, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets = append(f.sets, host+"="+map[bool]string{true: "true", false: "false"}[on])
	if f.setErr != nil {
		for i := range f.devs {
			if f.devs[i].Host == host {
				f.devs[i].Err = f.setErr.Error() // the Exporter records the error on the device
			}
		}
		return f.setErr
	}
	for i := range f.devs {
		if f.devs[i].Host == host {
			f.devs[i].On = on
		}
	}
	return nil
}

func (f *fakeDevices) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	f.mu.Lock()
	f.subs[ch] = true
	f.mu.Unlock()
	return ch, func() {
		f.mu.Lock()
		delete(f.subs, ch)
		f.mu.Unlock()
		f.unsubscribed.Add(1)
	}
}

// change replaces the devices and wakes the subscribers.
func (f *fakeDevices) change(devs ...exporter.DeviceView) {
	f.mu.Lock()
	f.devs = devs
	for ch := range f.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	f.mu.Unlock()
}

func fl(v float64) *float64 { return &v }

func dev(host, alias string, watts float64) exporter.DeviceView {
	return exporter.DeviceView{Host: host, Alias: alias, Model: "KP125(US)", Up: true, On: true, HasState: true,
		PowerWatts: fl(watts), RSSI: fl(-60)}
}
