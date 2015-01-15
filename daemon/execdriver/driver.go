package execdriver

import (
	"errors"
	"io"
	"os"
	"os/exec"

	"github.com/docker/libcontainer/devices"
)

// Context is a generic key value pair that allows
// arbatrary data to be sent
type Context map[string]string

var (
	ErrNotRunning              = errors.New("Process could not be started")
	ErrWaitTimeoutReached      = errors.New("Wait timeout reached")
	ErrDriverAlreadyRegistered = errors.New("A driver already registered this docker init function")
	ErrDriverNotFound          = errors.New("The requested docker init has not been found")
)

type StartCallback func(*ProcessConfig, int)

// Driver specific information based on
// processes registered with the driver
type Info interface {
	IsRunning() bool
}

// Terminal in an interface for drivers to implement
// if they want to support Close and Resize calls from
// the core
type Terminal interface {
	io.Closer
	Resize(height, width int) error
}

type TtyTerminal interface {
	Master() *os.File
}

// ExitStatus provides exit reasons for a container.
type ExitStatus struct {
	// The exit code with which the container exited.
	ExitCode int

	// Whether the container encountered an OOM.
	OOMKilled bool
}

type Driver interface {
	Run(c *Command, pipes *Pipes, startCallback StartCallback) (ExitStatus, error) // Run executes the process and blocks until the process exits and returns the exit code
	// Exec executes the process in an existing container, blocks until the process exits and returns the exit code
	Exec(c *Command, processConfig *ProcessConfig, pipes *Pipes, startCallback StartCallback) (int, error)
	Kill(c *Command, sig int) error
	Pause(c *Command) error
	Unpause(c *Command) error
	Name() string                                 // Driver name
	Info(id string) Info                          // "temporary" hack (until we move state from core to plugins)
	GetPidsForContainer(id string) ([]int, error) // Returns a list of pids for the given container.
	Terminate(c *Command) error                   // kill it with fire
	Clean(id string) error                        // clean all traces of container exec
	Checkpoint(checkpoint *Checkpoint, stop bool) error
	Restore(checkpoint *Checkpoint, pipes *Pipes, startCallback StartCallback) (ExitStatus, error)
}

// Network settings of the container
type Network struct {
	Interface      *NetworkInterface `json:"interface"` // if interface is nil then networking is disabled
	Mtu            int               `json:"mtu"`
	ContainerID    string            `json:"container_id"` // id of the container to join network.
	HostNetworking bool              `json:"host_networking"`
}

// IPC settings of the container
type Ipc struct {
	ContainerID string `json:"container_id"` // id of the container to join ipc.
	HostIpc     bool   `json:"host_ipc"`
}

// PID settings of the container
type Pid struct {
	HostPid bool `json:"host_pid"`
}

type NetworkInterface struct {
	Gateway              string `json:"gateway"`
	IPAddress            string `json:"ip"`
	IPPrefixLen          int    `json:"ip_prefix_len"`
	MacAddress           string `json:"mac"`
	Bridge               string `json:"bridge"`
	GlobalIPv6Address    string `json:"global_ipv6"`
	LinkLocalIPv6Address string `json:"link_local_ipv6"`
	GlobalIPv6PrefixLen  int    `json:"global_ipv6_prefix_len"`
	IPv6Gateway          string `json:"ipv6_gateway"`
}

type Resources struct {
	Memory     int64  `json:"memory"`
	MemorySwap int64  `json:"memory_swap"`
	CpuShares  int64  `json:"cpu_shares"`
	Cpuset     string `json:"cpuset"`
}

type Mount struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Writable    bool   `json:"writable"`
	Private     bool   `json:"private"`
	Slave       bool   `json:"slave"`
}

// Describes a process that will be run inside a container.
type ProcessConfig struct {
	exec.Cmd `json:"-"`

	Privileged bool     `json:"privileged"`
	User       string   `json:"user"`
	Tty        bool     `json:"tty"`
	Entrypoint string   `json:"entrypoint"`
	Arguments  []string `json:"arguments"`
	Terminal   Terminal `json:"-"` // standard or tty terminal
	Console    string   `json:"-"` // dev/console path
}

// Process wrapps an os/exec.Cmd to add more metadata
type Command struct {
	ID                 string            `json:"id"`
	Rootfs             string            `json:"rootfs"` // root fs of the container
	ReadonlyRootfs     bool              `json:"readonly_rootfs"`
	InitPath           string            `json:"initpath"` // dockerinit
	WorkingDir         string            `json:"working_dir"`
	ConfigPath         string            `json:"config_path"` // this should be able to be removed when the lxc template is moved into the driver
	Network            *Network          `json:"network"`
	Ipc                *Ipc              `json:"ipc"`
	Pid                *Pid              `json:"pid"`
	Resources          *Resources        `json:"resources"`
	Mounts             []Mount           `json:"mounts"`
	AllowedDevices     []*devices.Device `json:"allowed_devices"`
	AutoCreatedDevices []*devices.Device `json:"autocreated_devices"`
	CapAdd             []string          `json:"cap_add"`
	CapDrop            []string          `json:"cap_drop"`
	ContainerPid       int               `json:"container_pid"`  // the pid for the process inside a container
	ProcessConfig      ProcessConfig     `json:"process_config"` // Describes the init process of the container.
	ProcessLabel       string            `json:"process_label"`
	MountLabel         string            `json:"mount_label"`
	LxcConfig          []string          `json:"lxc_config"`
	AppArmorProfile    string            `json:"apparmor_profile"`
}

// Checkpoint context
type Checkpoint struct {
	Command   *Command
	ImagePath string
	Volumes   map[string]string
}
