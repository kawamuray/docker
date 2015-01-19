package main

import (
	"bytes"
	"os"
	"os/exec"
	"io"
	"io/ioutil"
	"net"
	"fmt"
	"path/filepath"
	"encoding/binary"
	"encoding/hex"
	"regexp"
	"unsafe"
	"strings"
	criu_pb "./criu"
	"github.com/golang/protobuf/proto"
)

var native binary.ByteOrder

func init() {
	var x uint32 = 0x01020304
	if *(*byte)(unsafe.Pointer(&x)) == 0x01 {
		native = binary.BigEndian
	} else {
		native = binary.LittleEndian
	}
}

func rewriteMacAddress(srcPath, destPath, mac string) error {
	srcFp, err := os.Open(filepath.Join(srcPath, "netdev-8.img"))
	if err != nil {
		return err
	}
	defer srcFp.Close()

	destFilePath := filepath.Join(destPath, "netdev-8.img")
	os.Remove(destFilePath)
	destFp, err := os.OpenFile(destFilePath, os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer destFp.Close()

	// Copy magic header first
	if _, err := io.CopyN(destFp, srcFp, 4); err != nil {
		return err
	}

	// Checkout each device
	for {
		var (
			size uint32
			device criu_pb.NetDeviceEntry
		)
		if err := binary.Read(srcFp, native, &size); err != nil {
			if err == io.EOF {
				break
			} else {
				return err
			}
		}

		buf := make([]byte, size)
		if _, err := io.ReadFull(srcFp, buf); err != nil {
			return err
		}
		if err := proto.Unmarshal(buf, &device); err != nil {
			return err
		}
		if device.Name != nil && *device.Name == "eth0" {
			// TODO only ipv4
			macHex, err := hex.DecodeString(mac)
			if err != nil {
				return err
			}
			device.Address = macHex
		}
		data, err := proto.Marshal(&device)
		if err != nil {
			return err
		}

		size = uint32(len(data))
		if err := binary.Write(destFp, native, &size); err != nil {
			return err
		}
		if _, err := destFp.Write(data); err != nil {
			return err
		}
	}
	return nil
}

func rewriteBytes(data, from, to []byte) int {
	replaced := 0
	for {
		pos := bytes.Index(data, from)
		if pos == -1 {
			break
		}
		copy(data[pos:pos+len(to)], to)
		replaced++
	}
	return replaced
}

func replaceWrite(path string, data []byte) error {
	os.Remove(path)
	return ioutil.WriteFile(path, data, 0644)
}

func rewriteIPAddress(srcPath, destPath, ip string) error {
	// TODO obviously incomplete implementation
	data, err := ioutil.ReadFile(filepath.Join(srcPath, "ifaddr-8.img"))
	if err != nil {
		return err
	}

	ipCmd := exec.Command("ip", "addr", "showdump")
	ipCmd.Stdin = bytes.NewBuffer(data)
	dump, err := ipCmd.Output()
	if err != nil {
		return err
	}
	// fmt.Println(string(dump))

	found := regexp.MustCompile(`    inet ((?:[0-9]{0,3}\.){3}[0-9]{0,3})/[0-9]+ scope global eth0`).FindSubmatch(dump)
	if found == nil {
		return fmt.Errorf("can't find old inet address")
	}

	oldAddress := net.ParseIP(string(found[1])).To4()
	// fmt.Println(oldAddress)

	newAddress := net.ParseIP(ip)
	if newAddress == nil {
		return fmt.Errorf("can't parse %s as an IPv4 Address", ip)
	}
	newAddress = newAddress.To4()

	if rewriteBytes(data, oldAddress, newAddress) < 1 {
		return fmt.Errorf("can't find old address pos in ip addr dump")
	}
	if err := replaceWrite(filepath.Join(destPath, "ifaddr-8.img"), data); err != nil {
		return err
	}

	routeData, err := ioutil.ReadFile(filepath.Join(srcPath, "route-8.img"))
	if err != nil {
		return err
	}
	if rewriteBytes(routeData, oldAddress, newAddress) < 1 {
		return fmt.Errorf("can't find old address pos in ip route dump")
	}
	return replaceWrite(filepath.Join(destPath, "route-8.img"), routeData)
}

func rewriteCgroupDirEntry(dir *criu_pb.CgroupDirEntry, fromPattern, toPattern string) {
	*dir.DirName = strings.Replace(*dir.DirName, fromPattern, toPattern, -1)
	for _, child := range dir.Children {
		rewriteCgroupDirEntry(child, fromPattern, toPattern)
	}
}

func rewriteCgroupPaths(srcPath, destPath, fromPattern, toPattern string) error {
	srcFp, err := os.Open(filepath.Join(srcPath, "cgroup.img"))
	if err != nil {
		return err
	}
	defer srcFp.Close()

	destFilePath := filepath.Join(destPath, "cgroup.img")
	os.Remove(destFilePath)
	destFp, err := os.OpenFile(destFilePath, os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer destFp.Close()

	// Copy magic header first
	if _, err := io.CopyN(destFp, srcFp, 4); err != nil {
		return err
	}

	for {
		var (
			size uint32
			cgroupEntry criu_pb.CgroupEntry
		)
		if err := binary.Read(srcFp, native, &size); err != nil {
			if err == io.EOF {
				break
			} else {
				return err
			}
		}

		buf := make([]byte, size)
		if _, err := io.ReadFull(srcFp, buf); err != nil {
			return err
		}
		if err := proto.Unmarshal(buf, &cgroupEntry); err != nil {
			return err
		}

		for _, set := range cgroupEntry.Sets {
			for _, ctl := range set.Ctls {
				*ctl.Path = strings.Replace(*ctl.Path, fromPattern, toPattern, -1)
			}
		}
		for _, controller := range cgroupEntry.Controllers {
			for _, dir := range controller.Dirs {
				rewriteCgroupDirEntry(dir, fromPattern, toPattern)
			}
		}

		data, err := proto.Marshal(&cgroupEntry)
		if err != nil {
			return err
		}

		size = uint32(len(data))
		if err := binary.Write(destFp, native, &size); err != nil {
			return err
		}
		if _, err := destFp.Write(data); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s SRC_DIR DEST_DIR ip=NEW_IPADDR mac=NEW_MACADDR cgroup=OLD_CGROUP_PATTERN:NEW_CGROUP_PATTERN\n", os.Args[0])
		os.Exit(1)
	}

	srcPath := os.Args[1]
	destPath := os.Args[2]
	var err error
	for _, spec := range os.Args[3:] {
		kv := strings.SplitN(spec, "=", 2)
		switch kv[0] {
		case "ip":
			err = rewriteIPAddress(srcPath, destPath, kv[1])
		case "mac":
			err = rewriteMacAddress(srcPath, destPath, kv[1])
		case "cgroup":
			oldAndNew := strings.SplitN(kv[1], ":", 2)
			if len(oldAndNew) < 2 {
				err = fmt.Errorf("invalid cgroup= parameter")
			} else {
				err = rewriteCgroupPaths(srcPath, destPath, oldAndNew[0], oldAndNew[1])
			}
		default:
			err = fmt.Errorf("unkown key: %s", kv[0])
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", err)
			os.Exit(1)
		}
	}
}
