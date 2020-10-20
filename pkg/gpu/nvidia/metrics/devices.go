package metrics

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"time"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/nvml"

	"github.com/golang/glog"
	"google.golang.org/grpc"
	podresources "k8s.io/kubernetes/pkg/kubelet/apis/podresources/v1alpha1"
)

var (
	socketPath      = "/var/lib/kubelet/pod-resources/kubelet.sock"
	gpuResourceName = "nvidia.com/gpu"
	gpuPathRegex    = regexp.MustCompile("/dev/(nvidia[0-9]+)$")

	connectionTimeout = 10 * time.Second

	gpuDevices map[string]*nvml.Device
)

// ContainerID uniquely identifies a container.
type ContainerID struct {
	namespace string
	pod       string
	container string
}

// GetDevicesForAllContainers returns a map with container as the key and the list of devices allocated to that container as the value.
func GetDevicesForAllContainers() (map[ContainerID][]string, error) {
	containerDevices := make(map[ContainerID][]string)
	conn, err := grpc.Dial(
		socketPath,
		grpc.WithInsecure(),
		grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("unix", addr, timeout)
		}))
	if err != nil {
		return containerDevices, fmt.Errorf("error connecting to kubelet PodResourceLister service: %v", err)
	}
	client := podresources.NewPodResourcesListerClient(conn)

	resp, err := client.List(context.Background(), &podresources.ListPodResourcesRequest{})
	if err != nil {
		return containerDevices, fmt.Errorf("error listing pod resources: %v", err)
	}

	for _, pod := range resp.PodResources {
		container := ContainerID{
			namespace: pod.Namespace,
			pod:       pod.Name,
		}

		for _, c := range pod.Containers {
			container.container = c.Name
			for _, d := range c.Devices {
				if len(d.DeviceIds) == 0 || d.ResourceName != gpuResourceName {
					continue
				}
				containerDevices[container] = make([]string, 0)
				for _, deviceID := range d.DeviceIds {
					containerDevices[container] = append(containerDevices[container], deviceID)
				}
			}
		}
	}

	return containerDevices, nil
}

// DiscoverGPUDevices discovers GPUs attached to the node, and updates `gpuDevices` map.
func DiscoverGPUDevices() error {
	count, err := nvml.GetDeviceCount()
	if err != nil {
		return fmt.Errorf("failed to get device count: %s", err)
	}

	glog.Infof("Foud %d GPU devices", count)
	gpuDevices = make(map[string]*nvml.Device)
	for i := uint(0); i < count; i++ {
		device, err := nvml.NewDevice(i)
		if err != nil {
			return fmt.Errorf("failed to read device with index %d: %v", i, err)
		}
		deviceName, err := deviceNameFromPath(device.Path)
		if err != nil {
			glog.Errorf("Invalid GPU device path found: %s. Skipping this device", device.Path)
		}
		glog.Infof("Found device %s for metrics collection", deviceName)
		gpuDevices[deviceName] = device
	}

	return nil
}

// DeviceFromName returns the device object for a given device name.
func DeviceFromName(deviceName string) (nvml.Device, error) {
	device, ok := gpuDevices[deviceName]
	if !ok {
		return nvml.Device{}, fmt.Errorf("device %s not found", deviceName)
	}

	return *device, nil
}

// DeviceUtilzation returns the current utilization of the GPU device.
func DeviceUtilzation(deviceName string) (nvml.UtilizationInfo, error) {
	device, ok := gpuDevices[deviceName]
	if !ok {
		return nvml.UtilizationInfo{}, fmt.Errorf("device %s not found", deviceName)
	}

	status, err := device.Status()
	if err != nil {
		return nvml.UtilizationInfo{}, fmt.Errorf("failed to get status for device %s: %v", deviceName, err)
	}

	return status.Utilization, nil
}

func deviceNameFromPath(path string) (string, error) {
	m := gpuPathRegex.FindStringSubmatch(path)
	if len(m) != 2 {
		return "", fmt.Errorf("path (%s) is not a valid GPU device path", path)
	}
	return m[1], nil
}
