// Copyright 2017 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package metrics

import (
	"fmt"
	"net/http"
	"time"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/nvml"
	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// DutyCycle reports the percent of time when the GPU was actively processing.
	DutyCycle = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "duty_cycle",
			Help: "Percent of time when the GPU was actively processing",
		},
		[]string{"namespace", "pod", "container", "make", "accelerator_id", "model"})

	// MemoryTotal reports the total memory available on the GPU.
	MemoryTotal = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "memory_total",
			Help: "Total memory available on the GPU in bytes",
		},
		[]string{"namespace", "pod", "container", "make", "accelerator_id", "model"})

	// MemoryUsed reports GPU memory allocated.
	MemoryUsed = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "memory_used",
			Help: "Allocated GPU memory in bytes",
		},
		[]string{"namespace", "pod", "container", "make", "accelerator_id", "model"})

	// AcceleratorRequests reports the number of GPU devices requested by the container.
	AcceleratorRequests = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "accelerator_requests",
			Help: "Number of accelerator devices requested by the container",
		},
		[]string{"namespace", "pod", "container", "resource_name"})
)

// MetricServer exposes GPU metrics for all containers in prometheus format on the specified port.
type MetricServer struct {
	collectionInterval  int
	port                int
	metricsEndpointPath string
}

func NewMetricServer(collectionInterval, port int, metricsEndpointPath string) *MetricServer {
	return &MetricServer{
		collectionInterval:  collectionInterval,
		port:                port,
		metricsEndpointPath: metricsEndpointPath,
	}
}

// Start performs necessary initializations and starts the metric server.
func (m *MetricServer) Start() error {
	glog.Infoln("Starting metrics server")

	err := nvml.Init()
	if err != nil {
		return fmt.Errorf("failed to initialize nvml: %v", err)
	}

	driverVersion, err := nvml.GetDriverVersion()
	if err != nil {
		return fmt.Errorf("failed to query nvml: %v", err)
	}
	glog.Infof("nvml initialized successfully. Driver version: %s", driverVersion)

	err = DiscoverGPUDevices()
	if err != nil {
		return fmt.Errorf("failed to discover GPU devices: %v", err)
	}

	go func() {
		http.Handle(m.metricsEndpointPath, promhttp.Handler())
		err := http.ListenAndServe(fmt.Sprintf(":%d", m.port), nil)
		if err != nil {
			glog.Infof("Failed to start metric server: %v", err)
		}
	}()

	go m.collectMetrics()
	return nil
}

func (m *MetricServer) collectMetrics() {
	t := time.NewTicker(time.Millisecond * time.Duration(m.collectionInterval))
	defer t.Stop()

	for {
		select {
		case <-t.C:
			devices, err := GetDevicesForAllContainers()
			if err != nil {
				glog.Errorf("Failed to get devices for containers: %v", err)
				continue
			}
			m.updateMetrics(devices)
		}
	}
}

func (m *MetricServer) updateMetrics(containerDevices map[ContainerID][]string) {
	for container, devices := range containerDevices {
		AcceleratorRequests.WithLabelValues(container.namespace, container.pod, container.container, gpuResourceName).Set(float64(len(devices)))

		for _, device := range devices {
			d, err := DeviceFromName(device)
			if err != nil {
				glog.Errorf("Failed to get device for %s: %v", device, err)
				continue
			}

			status, err := d.Status()
			if err != nil {
				glog.Errorf("Failed to get device status for %s: %v", device, err)
			}

			utilInfo := status.Utilization

			DutyCycle.WithLabelValues(container.namespace, container.pod, container.container, "nvidia", d.UUID, *d.Model).Set(float64(*utilInfo.GPU))
			MemoryTotal.WithLabelValues(container.namespace, container.pod, container.container, "nvidia", d.UUID, *d.Model).Set(float64(*d.Memory))
			MemoryUsed.WithLabelValues(container.namespace, container.pod, container.container, "nvidia", d.UUID, *d.Model).Set(float64(*utilInfo.Memory))
		}
	}
}

// Stop performs cleanup operations and stops the metric server.
func (m *MetricServer) Stop() {
	nvml.Shutdown()
}
