package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/devicelab-dev/maestro-runner/pkg/emulator"
	"github.com/devicelab-dev/maestro-runner/pkg/logger"
	"github.com/devicelab-dev/maestro-runner/pkg/simulator"
)

const (
	BootedByDaemon   = "daemon"
	BootedByExternal = "external"
	defaultBootSecs  = 120
)

// Registry tracks the devices a daemon booted or attached and persists them
// to devices.json so a crashed daemon leaves a record (`ps`, `device stop
// --orphans`). Live discovery (adb / simctl) is injected so this package
// stays independent of pkg/cli.
type Registry struct {
	mu      sync.Mutex
	name    string
	records map[string]*DeviceInfo
	sim     *simulator.Manager
	emu     *emulator.Manager
	list    func(platform string) []DeviceInfo
	// booting guards concurrent boots of the same name.
	booting map[string]bool
}

// NewRegistry creates a registry for daemon name. list discovers live
// devices (may be nil for tests).
func NewRegistry(name string, list func(platform string) []DeviceInfo) *Registry {
	return &Registry{
		name:    name,
		records: map[string]*DeviceInfo{},
		sim:     simulator.NewManager(),
		emu:     emulator.NewManager(),
		list:    list,
		booting: map[string]bool{},
	}
}

// Live returns the devices currently visible to adb / simctl. Platform ""
// means all.
func (r *Registry) Live(platform string) []DeviceInfo {
	if r.list == nil {
		return nil
	}
	return r.list(platform)
}

// List merges live devices with the registry: every live device appears,
// tagged with bootedBy/attached from the records; records for devices that
// vanished are dropped.
func (r *Registry) List(platform string) []DeviceInfo {
	live := r.Live(platform)
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]bool{}
	out := make([]DeviceInfo, 0, len(live))
	for _, d := range live {
		seen[d.ID] = true
		if rec, ok := r.records[d.ID]; ok {
			d.BootedBy = rec.BootedBy
			d.BootedAt = rec.BootedAt
			d.Attached = rec.Attached
			d.AttachedAt = rec.AttachedAt
			d.AppID = rec.AppID
			d.Driver = rec.Driver
			d.ReportDir = rec.ReportDir
			d.Busy = rec.Busy
		} else {
			d.BootedBy = BootedByExternal
		}
		out = append(out, d)
	}
	// Records the live list can't see (mock devices, or a device that
	// dropped off adb while attached) still show so they can be detached.
	for id, rec := range r.records {
		if seen[id] {
			continue
		}
		if platform != "" && rec.Platform != platform {
			continue
		}
		if rec.Attached || rec.Platform == "mock" || rec.Platform == "web" {
			out = append(out, *rec)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Platform != out[j].Platform {
			return out[i].Platform < out[j].Platform
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Lookup returns the merged info for one device.
func (r *Registry) Lookup(id string) (DeviceInfo, bool) {
	for _, d := range r.List("") {
		if d.ID == id {
			return d, true
		}
	}
	return DeviceInfo{}, false
}

// Records returns the persisted records (a copy).
func (r *Registry) Records() []DeviceInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked()
}

func (r *Registry) snapshotLocked() []DeviceInfo {
	out := make([]DeviceInfo, 0, len(r.records))
	for _, rec := range r.records {
		out = append(out, *rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// persistLocked rewrites devices.json.
func (r *Registry) persistLocked() {
	if r.name == "" {
		return
	}
	if err := os.MkdirAll(RunDir(r.name), 0o700); err != nil {
		logger.Warn("devices.json: %v", err)
		return
	}
	if err := atomicWriteJSON(DevicesPath(r.name), r.snapshotLocked()); err != nil {
		logger.Warn("devices.json: %v", err)
	}
}

// MarkAttached records that the daemon holds a driver for the device.
func (r *Registry) MarkAttached(info DeviceInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[info.ID]
	if !ok {
		rec = &DeviceInfo{ID: info.ID, BootedBy: BootedByExternal}
		r.records[info.ID] = rec
	}
	if info.Platform != "" {
		rec.Platform = info.Platform
	}
	if info.Kind != "" {
		rec.Kind = info.Kind
	}
	if info.Name != "" {
		rec.Name = info.Name
	}
	rec.Attached = true
	rec.AttachedAt = nowRFC3339()
	rec.AppID = info.AppID
	rec.Driver = info.Driver
	rec.ReportDir = info.ReportDir
	rec.Busy = false
	r.persistLocked()
}

// MarkDetached clears the attachment; a record that was only there for
// the attachment is dropped.
func (r *Registry) MarkDetached(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[id]
	if !ok {
		return
	}
	rec.Attached = false
	rec.AttachedAt = ""
	rec.AppID = ""
	rec.Driver = ""
	rec.Busy = false
	if rec.BootedBy != BootedByDaemon {
		delete(r.records, id)
	}
	r.persistLocked()
}

// SetBusy flags a step in flight (not persisted).
func (r *Registry) SetBusy(id string, busy bool) {
	r.mu.Lock()
	if rec, ok := r.records[id]; ok {
		rec.Busy = busy
	}
	r.mu.Unlock()
}

// Boot starts a simulator or emulator and records it as daemon-booted. A
// device that is already running under that name/UDID is returned as-is
// (bootedBy external) rather than booted twice.
func (r *Registry) Boot(ctx context.Context, req BootRequest) (DeviceInfo, error) {
	if req.Name == "" {
		return DeviceInfo{}, Errorf(CodeUsage, "device start: a simulator/AVD name or UDID is required")
	}
	timeout := time.Duration(req.BootTimeout) * time.Second
	if timeout <= 0 {
		timeout = defaultBootSecs * time.Second
	}
	platform := strings.ToLower(req.Platform)
	if platform == "" {
		platform = guessBootPlatform(req.Name)
	}

	// Already running?
	for _, d := range r.Live(platform) {
		if d.Ready && (d.ID == req.Name || strings.EqualFold(d.Name, req.Name)) {
			logger.Info("device start: %s already running as %s", req.Name, d.ID)
			return d, nil
		}
	}

	key := platform + ":" + req.Name
	r.mu.Lock()
	if r.booting[key] {
		r.mu.Unlock()
		return DeviceInfo{}, Errorf(CodeBusy, "device start: %s is already booting", req.Name)
	}
	r.booting[key] = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.booting, key)
		r.mu.Unlock()
	}()

	var id string
	var err error
	done := make(chan struct{})
	go func() {
		defer close(done)
		switch platform {
		case "ios":
			if simulator.IsSimulator(req.Name) {
				id, err = r.sim.Start(req.Name, timeout)
			} else {
				id, err = r.sim.StartByName(req.Name, timeout)
			}
		case "android":
			id, err = r.emu.Start(req.Name, timeout)
		default:
			err = Errorf(CodeUsage, "device start: platform must be ios or android (got %q)", req.Platform)
		}
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return DeviceInfo{}, Errorf(CodeInterrupted, "device start: cancelled while booting %s", req.Name)
	}
	if err != nil {
		if de, ok := err.(*Error); ok {
			return DeviceInfo{}, de
		}
		return DeviceInfo{}, WrapErr(CodeDeviceError, err)
	}

	info := DeviceInfo{ID: id, Platform: platform, Name: req.Name, State: "Booted", Ready: true,
		BootedBy: BootedByDaemon, BootedAt: nowRFC3339()}
	if platform == "ios" {
		info.Kind = "simulator"
	} else {
		info.Kind = "emulator"
	}
	if live, ok := r.findLive(platform, id); ok {
		info.Name, info.OSVersion, info.State, info.Ready = live.Name, live.OSVersion, live.State, live.Ready
	}
	r.mu.Lock()
	if rec, ok := r.records[id]; ok {
		rec.BootedBy, rec.BootedAt = BootedByDaemon, info.BootedAt
		rec.Platform, rec.Kind, rec.Name = info.Platform, info.Kind, info.Name
	} else {
		cp := info
		r.records[id] = &cp
	}
	r.persistLocked()
	r.mu.Unlock()
	return info, nil
}

func (r *Registry) findLive(platform, id string) (DeviceInfo, bool) {
	for _, d := range r.Live(platform) {
		if d.ID == id {
			return d, true
		}
	}
	return DeviceInfo{}, false
}

// guessBootPlatform picks ios for simulator UDIDs / names containing
// "iPhone"/"iPad", android otherwise.
func guessBootPlatform(name string) string {
	if simulator.IsSimulator(name) {
		return "ios"
	}
	lower := strings.ToLower(name)
	if strings.Contains(lower, "iphone") || strings.Contains(lower, "ipad") || strings.Contains(lower, "apple") {
		return "ios"
	}
	return "android"
}

// Stop shuts a simulator/emulator down. External devices need force; a
// physical device can't be stopped at all. The caller must have detached
// the device first.
func (r *Registry) Stop(id string, force bool) error {
	info, ok := r.Lookup(id)
	if !ok {
		return Errorf(CodeDeviceNotFound, "device %s not found", id)
	}
	if info.Attached {
		return Errorf(CodeBusy, "device %s is attached; detach it first", id)
	}
	if info.BootedBy != BootedByDaemon && !force {
		return Errorf(CodeDeviceExternal, "device %s was not started by this daemon; use --force to stop it", id).
			WithDetails("bootedBy", info.BootedBy)
	}
	if err := shutdownDevice(info); err != nil {
		return err
	}
	r.mu.Lock()
	delete(r.records, id)
	r.persistLocked()
	r.mu.Unlock()
	return nil
}

// shutdownDevice shuts a simulator or emulator down regardless of who
// booted it.
func shutdownDevice(info DeviceInfo) error {
	switch {
	case info.Platform == "ios" && (info.Kind == "simulator" || simulator.IsSimulator(info.ID)):
		if err := simulator.ShutdownSimulator(info.ID, 60*time.Second); err != nil {
			return WrapErr(CodeDeviceError, err)
		}
	case info.Platform == "android" && (info.Kind == "emulator" || emulator.IsEmulator(info.ID)):
		if err := emulator.ShutdownEmulator(info.ID, 60*time.Second); err != nil {
			return WrapErr(CodeDeviceError, err)
		}
	case info.Platform == "mock" || info.Platform == "web":
		return nil
	default:
		return Errorf(CodeUsage, "device %s is a physical device and cannot be stopped", info.ID)
	}
	return nil
}

// ShutdownOwned stops every device this daemon booted that is still
// running. Errors are logged; the daemon is exiting anyway.
func (r *Registry) ShutdownOwned() {
	r.mu.Lock()
	var owned []DeviceInfo
	for _, rec := range r.records {
		if rec.BootedBy == BootedByDaemon {
			owned = append(owned, *rec)
		}
	}
	r.mu.Unlock()
	var wg sync.WaitGroup
	for _, d := range owned {
		wg.Add(1)
		go func(d DeviceInfo) {
			defer wg.Done()
			logger.Info("Shutting down daemon-booted device %s", d.ID)
			if err := shutdownDevice(d); err != nil {
				logger.Warn("shutdown %s: %v", d.ID, err)
			}
			r.mu.Lock()
			delete(r.records, d.ID)
			r.persistLocked()
			r.mu.Unlock()
		}(d)
	}
	wg.Wait()
}

// StopOrphans shuts down devices recorded as daemon-booted by daemons that
// are no longer alive and removes their run files. It returns the device
// IDs it stopped. Used by `device stop --orphans`; needs no daemon.
func StopOrphans() ([]string, error) {
	daemons, err := ListDaemons()
	if err != nil {
		return nil, err
	}
	var stopped []string
	var errs []error
	for _, d := range daemons {
		if d.Alive {
			continue
		}
		for _, dev := range d.Devices {
			if dev.BootedBy != BootedByDaemon {
				continue
			}
			if err := shutdownDevice(dev); err != nil {
				errs = append(errs, fmt.Errorf("%s (%s): %w", dev.ID, d.Name, err))
				continue
			}
			stopped = append(stopped, dev.ID)
		}
		RemoveRunFiles(d.Name)
	}
	return stopped, errors.Join(errs...)
}
