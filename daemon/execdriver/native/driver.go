// +build linux,cgo

package native

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"strconv"
	"sync"
	"syscall"

	log "github.com/Sirupsen/logrus"
	"github.com/docker/docker/daemon/execdriver"
	"github.com/docker/docker/pkg/term"
	"github.com/docker/libcontainer"
	"github.com/docker/libcontainer/apparmor"
	"github.com/docker/libcontainer/cgroups/fs"
	"github.com/docker/libcontainer/cgroups/systemd"
	consolepkg "github.com/docker/libcontainer/console"
	"github.com/docker/libcontainer/namespaces"
	_ "github.com/docker/libcontainer/namespaces/nsenter"
	"github.com/docker/libcontainer/system"
	"github.com/docker/libcontainer/utils"
	"github.com/docker/libcontainer/network"
)

const (
	DriverName = "native"
	Version    = "0.2"
)

type activeContainer struct {
	container *libcontainer.Config
	cmd       *exec.Cmd
}

type driver struct {
	root             string
	initPath         string
	activeContainers map[string]*activeContainer
	sync.Mutex
}

func NewDriver(root, initPath string) (*driver, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}

	// native driver root is at docker_root/execdriver/native. Put apparmor at docker_root
	if err := apparmor.InstallDefaultProfile(); err != nil {
		return nil, err
	}

	return &driver{
		root:             root,
		initPath:         initPath,
		activeContainers: make(map[string]*activeContainer),
	}, nil
}

type execOutput struct {
	exitCode int
	err      error
}

type execCallback func(container *libcontainer.Config, dataPath string, args []string, waitForStart chan struct{}) (int, error)

func (d *driver) exec(c *execdriver.Command, pipes *execdriver.Pipes, startCallback execdriver.StartCallback, container *libcontainer.Config, dataPath string, args []string, waitForStart chan struct{}) (int, error) {
	return namespaces.Exec(container, c.ProcessConfig.Stdin, c.ProcessConfig.Stdout, c.ProcessConfig.Stderr, c.ProcessConfig.Console, dataPath, args, func(container *libcontainer.Config, console, dataPath, init string, child *os.File, args []string) *exec.Cmd {
		c.ProcessConfig.Path = d.initPath
		c.ProcessConfig.Args = append([]string{
			DriverName,
			"-console", console,
			"-pipe", "3",
			"-root", filepath.Join(d.root, c.ID),
			"--",
		}, args...)

		// set this to nil so that when we set the clone flags anything else is reset
		c.ProcessConfig.SysProcAttr = &syscall.SysProcAttr{
			Cloneflags: uintptr(namespaces.GetNamespaceFlags(container.Namespaces)),
		}
		c.ProcessConfig.ExtraFiles = []*os.File{child}

		c.ProcessConfig.Env = container.Env
		c.ProcessConfig.Dir = container.RootFs

		return &c.ProcessConfig.Cmd
	}, func() {
		close(waitForStart)
		if startCallback != nil {
			c.ContainerPid = c.ProcessConfig.Process.Pid
			startCallback(&c.ProcessConfig, c.ContainerPid)
		}
	})
}

func (d *driver) run(c *execdriver.Command, pipes *execdriver.Pipes, executer execCallback) (execdriver.ExitStatus, error) {
	// take the Command and populate the libcontainer.Config from it
	container, err := d.createContainer(c)
	if err != nil {
		return execdriver.ExitStatus{ExitCode: -1}, err
	}

	var term execdriver.Terminal

	if c.ProcessConfig.Tty {
		term, err = NewTtyConsole(&c.ProcessConfig, pipes)
	} else {
		term, err = execdriver.NewStdConsole(&c.ProcessConfig, pipes)
	}
	if err != nil {
		return execdriver.ExitStatus{ExitCode: -1}, err
	}
	c.ProcessConfig.Terminal = term

	d.Lock()
	d.activeContainers[c.ID] = &activeContainer{
		container: container,
		cmd:       &c.ProcessConfig.Cmd,
	}
	d.Unlock()

	var (
		dataPath = filepath.Join(d.root, c.ID)
		args     = append([]string{c.ProcessConfig.Entrypoint}, c.ProcessConfig.Arguments...)
	)

	if err := d.createContainerRoot(c.ID); err != nil {
		return execdriver.ExitStatus{ExitCode: -1}, err
	}
	defer d.cleanContainer(c.ID)

	if err := d.writeContainerFile(container, c.ID); err != nil {
		return execdriver.ExitStatus{ExitCode: -1}, err
	}

	execOutputChan := make(chan execOutput, 1)
	waitForStart := make(chan struct{})

	go func() {
		exitCode, err := executer(container, dataPath, args, waitForStart)
		execOutputChan <- execOutput{exitCode, err}
	}()

	select {
	case execOutput := <-execOutputChan:
		return execdriver.ExitStatus{ExitCode: execOutput.exitCode}, execOutput.err
	case <-waitForStart:
		break
	}

	oomKill := false
	state, err := libcontainer.GetState(filepath.Join(d.root, c.ID))
	if err == nil {
		oomKillNotification, err := libcontainer.NotifyOnOOM(state)
		if err == nil {
			_, oomKill = <-oomKillNotification
		} else {
			log.Warnf("WARNING: Your kernel does not support OOM notifications: %s", err)
		}
	} else {
		log.Warnf("Failed to get container state, oom notify will not work: %s", err)
	}
	// wait for the container to exit.
	execOutput := <-execOutputChan
	log.Warnf("execOutput = %s", execOutput)

	return execdriver.ExitStatus{ExitCode: execOutput.exitCode, OOMKilled: oomKill}, execOutput.err
}

func (d *driver) Run(c *execdriver.Command, pipes *execdriver.Pipes, startCallback execdriver.StartCallback) (execdriver.ExitStatus, error) {
	return d.run(c, pipes, func(container *libcontainer.Config, dataPath string, args []string, waitForStart chan struct{}) (int, error) {
		return d.exec(c, pipes, startCallback, container, dataPath, args, waitForStart)
	})
}

func (d *driver) Kill(p *execdriver.Command, sig int) error {
	return syscall.Kill(p.ProcessConfig.Process.Pid, syscall.Signal(sig))
}

func (d *driver) Pause(c *execdriver.Command) error {
	active := d.activeContainers[c.ID]
	if active == nil {
		return fmt.Errorf("active container for %s does not exist", c.ID)
	}
	active.container.Cgroups.Freezer = "FROZEN"
	if systemd.UseSystemd() {
		return systemd.Freeze(active.container.Cgroups, active.container.Cgroups.Freezer)
	}
	return fs.Freeze(active.container.Cgroups, active.container.Cgroups.Freezer)
}

func (d *driver) Unpause(c *execdriver.Command) error {
	active := d.activeContainers[c.ID]
	if active == nil {
		return fmt.Errorf("active container for %s does not exist", c.ID)
	}
	active.container.Cgroups.Freezer = "THAWED"
	if systemd.UseSystemd() {
		return systemd.Freeze(active.container.Cgroups, active.container.Cgroups.Freezer)
	}
	return fs.Freeze(active.container.Cgroups, active.container.Cgroups.Freezer)
}

func (d *driver) Terminate(p *execdriver.Command) error {
	// lets check the start time for the process
	state, err := libcontainer.GetState(filepath.Join(d.root, p.ID))
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		// TODO: Remove this part for version 1.2.0
		// This is added only to ensure smooth upgrades from pre 1.1.0 to 1.1.0
		data, err := ioutil.ReadFile(filepath.Join(d.root, p.ID, "start"))
		if err != nil {
			// if we don't have the data on disk then we can assume the process is gone
			// because this is only removed after we know the process has stopped
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		state = &libcontainer.State{InitStartTime: string(data)}
	}

	currentStartTime, err := system.GetProcessStartTime(p.ProcessConfig.Process.Pid)
	if err != nil {
		return err
	}

	if state.InitStartTime == currentStartTime {
		err = syscall.Kill(p.ProcessConfig.Process.Pid, 9)
		syscall.Wait4(p.ProcessConfig.Process.Pid, nil, 0, nil)
	}
	d.cleanContainer(p.ID)

	return err

}

func (d *driver) Info(id string) execdriver.Info {
	return &info{
		ID:     id,
		driver: d,
	}
}

func (d *driver) Name() string {
	return fmt.Sprintf("%s-%s", DriverName, Version)
}

func (d *driver) GetPidsForContainer(id string) ([]int, error) {
	d.Lock()
	active := d.activeContainers[id]
	d.Unlock()

	if active == nil {
		return nil, fmt.Errorf("active container for %s does not exist", id)
	}
	c := active.container.Cgroups

	if systemd.UseSystemd() {
		return systemd.GetPids(c)
	}
	return fs.GetPids(c)
}

func (d *driver) writeContainerFile(container *libcontainer.Config, id string) error {
	data, err := json.Marshal(container)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(filepath.Join(d.root, id, "container.json"), data, 0655)
}

func (d *driver) cleanContainer(id string) error {
	d.Lock()
	delete(d.activeContainers, id)
	d.Unlock()
	return os.RemoveAll(filepath.Join(d.root, id, "container.json"))
}

func (d *driver) createContainerRoot(id string) error {
	return os.MkdirAll(filepath.Join(d.root, id), 0655)
}

func (d *driver) Clean(id string) error {
	return os.RemoveAll(filepath.Join(d.root, id))
}

func (d *driver) Checkpoint(checkpoint *execdriver.Checkpoint, stop bool) error {
	c := checkpoint.Command

	if d.activeContainers[c.ID] == nil {
		return fmt.Errorf("active container for %s does not exist", c.ID)
	}

	cmdArgs := []string{
		"dump",
		"-v4",
		"-o", "/dev/stdout",
		"--manage-cgroups",
		"--evasive-devices",
		"--ext-mount-map", "/etc/resolv.conf:/etc/resolv.conf",
		"--ext-mount-map", "/etc/hosts:/etc/hosts",
		"--ext-mount-map", "/etc/hostname:/etc/hostname",
		"--ext-mount-map", "/.dockerinit:/.dockerinit",
		"-D", checkpoint.ImagePath,
		"-t", fmt.Sprintf("%d", c.ContainerPid),
		"--root", c.Rootfs,
	}
	for hostPath, guestPath := range checkpoint.Volumes {
		cmdArgs = append(cmdArgs, "--ext-mount-map", hostPath+":"+guestPath)
	}
	output, err := exec.Command("criu", cmdArgs...).CombinedOutput()
	log.Warnf("Rootfs = %s", c.Rootfs)

	if err != nil {
		return fmt.Errorf("failed checkpointing container %s: %s; %s", c.ID, err, string(output))
	}
	return nil
}

func (d *driver) execRestore(checkpoint *execdriver.Checkpoint, pipes *execdriver.Pipes, startCallback execdriver.StartCallback, container *libcontainer.Config, dataPath string, args []string, waitForStart chan struct{}) (int, error) {
	c := checkpoint.Command

	pidFile := filepath.Join(checkpoint.ImagePath, "restore.pid")
	defer os.Remove(pidFile)

	vethName, _ := utils.GenerateRandomName("veth", 7)

	c.ProcessConfig.Path = "/usr/local/sbin/criu"
	c.ProcessConfig.Args = []string{
		"criu", "restore", "-v4",
		"-o", "/tmp/restore.log",
		"--restore-detached",
		"--restore-sibling",
		"--manage-cgroups",
		"--evasive-devices",
		"--ext-mount-map", fmt.Sprintf("/etc/resolv.conf:/var/lib/docker/containers/%s/resolv.conf", c.ID),
		"--ext-mount-map", fmt.Sprintf("/etc/hosts:/var/lib/docker/containers/%s/hosts", c.ID),
		"--ext-mount-map", fmt.Sprintf("/etc/hostname:/var/lib/docker/containers/%s/hostname", c.ID),
		"--ext-mount-map", "/.dockerinit:/var/lib/docker/init/dockerinit-1.0.1",
		"--veth-pair", fmt.Sprintf("eth0=%s", vethName),
		"--pidfile", pidFile,
		"-D", checkpoint.ImagePath,
		"--root", c.Rootfs,
	}
	// TODO take care of volumes
	if pipe, _ := c.ProcessConfig.StdinPipe(); pipe != nil {
		stat, _ := pipe.(*os.File).Stat()
		c.ProcessConfig.Args = append(c.ProcessConfig.Args, "--inherit-fd",
			fmt.Sprintf("fd[0]:pipe:[%d]", stat.Sys().(*syscall.Stat_t).Ino))
	}
	if pipe, _ := c.ProcessConfig.StdoutPipe(); pipe != nil {
		stat, _ := pipe.(*os.File).Stat()
		c.ProcessConfig.Args = append(c.ProcessConfig.Args, "--inherit-fd",
			fmt.Sprintf("fd[1]:pipe:[%d]", stat.Sys().(*syscall.Stat_t).Ino))
	}
	if pipe, _ := c.ProcessConfig.StderrPipe(); pipe != nil {
		stat, _ := pipe.(*os.File).Stat()
		c.ProcessConfig.Args = append(c.ProcessConfig.Args, "--inherit-fd",
			fmt.Sprintf("fd[2]:pipe:[%d]", stat.Sys().(*syscall.Stat_t).Ino))
	}

	// c.ProcessConfig.ExtraFiles = []*os.File{child}
	c.ProcessConfig.Env = container.Env
	c.ProcessConfig.Dir = container.RootFs

	defer func() {
		for _, subsys := range []string{
			"devices",
			"memory",
			"cpu",
			"cpuset",
			"cpuacct",
			"blkio",
			"perf_event",
			"freezer",
		} {
			path := fmt.Sprintf("/sys/fs/cgroup/%s/docker/%s", subsys, c.ID)
			if _, err := os.Stat(path); err == nil {
				os.Remove(path)
			}
		}
	}()

	if err := c.ProcessConfig.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.Sys().(syscall.WaitStatus).ExitStatus(), err
		} else {
			return -1, err
		}
	}
	log.Warnf("criu pid = %d", c.ProcessConfig.Process.Pid)

	// TODO there's possibly more than one network configs
	if err := network.SetInterfaceMaster(vethName, "docker0"); err != nil {
		return -1, err
	}
	if err := network.InterfaceUp(vethName); err != nil {
		return -1, err
	}

	close(waitForStart)
	sPid, err := ioutil.ReadFile(pidFile)
	if err != nil {
		return -1, err
	}

	pid, _ := strconv.Atoi(string(sPid))
	proc, err := os.FindProcess(pid)
	if err != nil {
		return -1, err
	}

	c.ProcessConfig.Process = proc
	if startCallback != nil {
		c.ContainerPid = pid
		startCallback(&c.ProcessConfig, c.ContainerPid)
	}

	log.Warnf("PROC = %s", proc)
	pState, err := proc.Wait()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			return -1, err
		}
	}

	log.Warnf("pState = %s", pState)
	exitCode := pState.Sys().(syscall.WaitStatus).ExitStatus()
	log.Warnf("exitCode = %d", exitCode)
	return exitCode, nil
}

func (d *driver) Restore(checkpoint *execdriver.Checkpoint, pipes *execdriver.Pipes, startCallback execdriver.StartCallback) (execdriver.ExitStatus, error) {
	return d.run(checkpoint.Command, pipes, func(container *libcontainer.Config, dataPath string, args []string, waitForStart chan struct{}) (int, error) {
		return d.execRestore(checkpoint, pipes, startCallback, container, dataPath, args, waitForStart)
	})
}


func getEnv(key string, env []string) string {
	for _, pair := range env {
		parts := strings.Split(pair, "=")
		if parts[0] == key {
			return parts[1]
		}
	}
	return ""
}

type TtyConsole struct {
	MasterPty *os.File
}

func NewTtyConsole(processConfig *execdriver.ProcessConfig, pipes *execdriver.Pipes) (*TtyConsole, error) {
	ptyMaster, console, err := consolepkg.CreateMasterAndConsole()
	if err != nil {
		return nil, err
	}

	tty := &TtyConsole{
		MasterPty: ptyMaster,
	}

	if err := tty.AttachPipes(&processConfig.Cmd, pipes); err != nil {
		tty.Close()
		return nil, err
	}

	processConfig.Console = console

	return tty, nil
}

func (t *TtyConsole) Master() *os.File {
	return t.MasterPty
}

func (t *TtyConsole) Resize(h, w int) error {
	return term.SetWinsize(t.MasterPty.Fd(), &term.Winsize{Height: uint16(h), Width: uint16(w)})
}

func (t *TtyConsole) AttachPipes(command *exec.Cmd, pipes *execdriver.Pipes) error {
	go func() {
		if wb, ok := pipes.Stdout.(interface {
			CloseWriters() error
		}); ok {
			defer wb.CloseWriters()
		}

		io.Copy(pipes.Stdout, t.MasterPty)
	}()

	if pipes.Stdin != nil {
		go func() {
			io.Copy(t.MasterPty, pipes.Stdin)

			pipes.Stdin.Close()
		}()
	}

	return nil
}

func (t *TtyConsole) Close() error {
	return t.MasterPty.Close()
}
