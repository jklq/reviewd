// Package plugin exposes harness drivers through HashiCorp go-plugin's local
// net/rpc transport. Plugin executables are trusted operator code, not sandboxes.
package plugin

import (
	"fmt"
	"io"
	"net/rpc"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"
	"github.com/jklq/reviewd/harness"
)

var handshake = goplugin.HandshakeConfig{ProtocolVersion: 1, MagicCookieKey: "REVIEWD_HARNESS_PLUGIN", MagicCookieValue: "reviewd-harness-v1"}

type driverPlugin struct{ Impl harness.Driver }

func (p *driverPlugin) Server(*goplugin.MuxBroker) (interface{}, error) { return &server{p.Impl}, nil }
func (*driverPlugin) Client(_ *goplugin.MuxBroker, c *rpc.Client) (interface{}, error) {
	return &client{rpc: c}, nil
}

// PrepareArgs is the version-1 wire request. Credentials stay on the server side
// of the Docker boundary and must never appear in plugin logs or launch fields.
type PrepareArgs struct {
	Options     harness.Options
	Credentials map[string]string
}
type server struct{ impl harness.Driver }

func (s *server) Validate(o harness.Options, _ *bool) error { return s.impl.Validate(o) }
func (s *server) Prepare(a PrepareArgs, result *harness.Launch) error {
	var err error
	*result, err = s.impl.Prepare(a.Options, a.Credentials)
	return err
}

type client struct {
	rpc     *rpc.Client
	process *goplugin.Client
}

func (c *client) call(method string, args, result interface{}) error {
	call := c.rpc.Go(method, args, result, make(chan *rpc.Call, 1))
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case done := <-call.Done:
		return done.Error
	case <-timer.C:
		if c.process != nil {
			c.process.Kill()
		} else {
			_ = c.rpc.Close()
		}
		return fmt.Errorf("harness plugin timed out")
	}
}
func (c *client) Validate(o harness.Options) error { return c.call("Plugin.Validate", o, new(bool)) }
func (c *client) Prepare(o harness.Options, values map[string]string) (harness.Launch, error) {
	var result harness.Launch
	if err := c.call("Plugin.Prepare", PrepareArgs{o, values}, &result); err != nil {
		// Third-party errors may contain serialized request arguments or credentials.
		return harness.Launch{}, fmt.Errorf("harness plugin preparation failed")
	}
	return result, nil
}

var processes = struct {
	sync.Mutex
	drivers map[string]*client
}{drivers: make(map[string]*client)}

// Open starts (or reuses) the executable and dispenses the driver keyed by its
// package path. No downloads, builds, shell expansion, or repository discovery.
func Open(name, path string) (harness.Driver, error) {
	if name == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("plugin requires a driver name and absolute executable path")
	}
	processes.Lock()
	defer processes.Unlock()
	key := path + "\x00" + name
	if c := processes.drivers[key]; c != nil {
		if c.process.Exited() {
			c.process.Kill()
			delete(processes.drivers, key)
		} else {
			return c, nil
		}
	}
	cmd := exec.Command(path)
	cmd.Env = []string{} // go-plugin adds its handshake and automatic mTLS environment.
	p := goplugin.NewClient(&goplugin.ClientConfig{
		HandshakeConfig: handshake, Plugins: goplugin.PluginSet{name: &driverPlugin{}},
		Cmd: cmd, AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolNetRPC},
		AutoMTLS: true, SkipHostEnv: true, StartTimeout: 10 * time.Second,
		Logger: hclog.NewNullLogger(), SyncStdout: io.Discard, SyncStderr: io.Discard,
	})
	protocol, err := p.Client()
	if err != nil {
		p.Kill()
		return nil, fmt.Errorf("start harness plugin: %w", err)
	}
	raw, err := protocol.Dispense(name)
	if err != nil {
		p.Kill()
		return nil, fmt.Errorf("dispense harness plugin %q: %w", name, err)
	}
	c := raw.(*client)
	c.process = p
	processes.drivers[key] = c
	return c, nil
}

// Close stops all plugin processes. The application calls this on every exit.
func Close() {
	processes.Lock()
	defer processes.Unlock()
	for key, c := range processes.drivers {
		c.process.Kill()
		delete(processes.drivers, key)
	}
}

// Serve is the complete main function for an independently built driver plugin.
func Serve(name string, driver harness.Driver) {
	goplugin.Serve(&goplugin.ServeConfig{HandshakeConfig: handshake, Plugins: goplugin.PluginSet{name: &driverPlugin{Impl: driver}}, Logger: hclog.NewNullLogger()})
}
