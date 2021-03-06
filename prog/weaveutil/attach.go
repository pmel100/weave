package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/vishvananda/netns"

	weavenet "github.com/weaveworks/weave/net"
	"github.com/weaveworks/weave/proxy"
)

func attach(args []string) error {
	if len(args) < 4 {
		cmdUsage("attach-container", "[--no-multicast-route] [--keep-tx-on] [--hairpin-mode=true|false] <container-id> <bridge-name> <mtu> <cidr>...")
	}

	keepTXOn := false
	withMulticastRoute := true
	hairpinMode := true
	for i := 0; i < len(args); {
		switch args[i] {
		case "--no-multicast-route":
			withMulticastRoute = false
			args = append(args[:i], args[i+1:]...)
		case "--keep-tx-on":
			keepTXOn = true
			args = append(args[:i], args[i+1:]...)
		case "--hairpin-mode=false":
			hairpinMode = false
			args = append(args[:i], args[i+1:]...)
		default:
			i++
		}
	}

	pid, err := containerPid(args[0])
	if err != nil {
		return err
	}
	nsContainer, err := netns.GetFromPid(pid)
	if err != nil {
		return fmt.Errorf("unable to open namespace for container %s: %s", args[0], err)
	}

	if nsHost, err := netns.GetFromPid(1); err != nil {
		return fmt.Errorf("unable to open host namespace: %s", err)
	} else if nsHost.Equal(nsContainer) {
		return fmt.Errorf("Container is running in the host network namespace, and therefore cannot be\nconnected to weave - perhaps the container was started with --net=host")
	}
	mtu, err := strconv.Atoi(args[2])
	if err != nil && args[3] != "" {
		return fmt.Errorf("unable to parse mtu %q: %s", args[2], err)
	}
	cidrs, err := parseCIDRs(args[3:])
	if err != nil {
		return err
	}

	err = weavenet.AttachContainer(weavenet.NSPathByPid(pid), fmt.Sprint(pid), weavenet.VethName, args[1], mtu, withMulticastRoute, cidrs, keepTXOn, hairpinMode)
	// If we detected an error but the container has died, tell the user that instead.
	if err != nil && !processExists(pid) {
		err = fmt.Errorf("Container %s died", args[0])
	}
	return err
}

func containerPid(containerID string) (int, error) {
	c, err := docker.NewVersionedClientFromEnv("1.18")
	if err != nil {
		return 0, fmt.Errorf("unable to connect to docker: %s", err)
	}
	container, err := c.InspectContainer(containerID)
	if err != nil {
		return 0, fmt.Errorf("unable to inspect container %s: %s", containerID, err)
	}
	if container.State.Pid == 0 {
		return 0, fmt.Errorf("container %s not running", containerID)
	}
	return container.State.Pid, nil
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, syscall.Signal(0))
	return err == nil || err == syscall.EPERM
}

func parseCIDRs(args []string) (cidrs []*net.IPNet, err error) {
	for _, ipstr := range args {
		ip, ipnet, err := net.ParseCIDR(ipstr)
		if err != nil {
			return nil, err
		}
		ipnet.IP = ip // we want the specific IP plus the mask
		cidrs = append(cidrs, ipnet)
	}
	return
}

func detach(args []string) error {
	if len(args) < 2 {
		cmdUsage("detach-container", "<container-id> <cidr>...")
	}

	pid, err := containerPid(args[0])
	if err != nil {
		return err
	}
	cidrs, err := parseCIDRs(args[1:])
	if err != nil {
		return err
	}
	return weavenet.DetachContainer(weavenet.NSPathByPid(pid), args[0], weavenet.VethName, cidrs)
}

func rewriteEtcHosts(args []string) error {
	if len(args) < 4 {
		cmdUsage("rewrite-etc-hosts", "<hosts-path> <fqdn> <image> <cidr>... [name:addr...]")
	}
	hostsPath := args[0]
	fqdn := args[1]
	image := args[2]
	var ips []*net.IPNet
	for _, cidr := range strings.Fields(args[3]) {
		_, ipnet, err := net.ParseCIDR(cidr)
		if err != nil {
			return err
		}
		ips = append(ips, ipnet)
	}
	extraHosts := args[4:]
	docker := os.Getenv("DOCKER_HOST")
	if docker == "" {
		docker = "unix:///var/run/docker.sock"
	}
	p, err := proxy.StubProxy(proxy.Config{DockerHost: docker, Image: image})
	if err != nil {
		return err
	}
	return p.RewriteEtcHosts(hostsPath, fqdn, ips, extraHosts)
}
