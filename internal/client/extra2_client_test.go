package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hospitus/hospitus/pkg/job"
	"github.com/hospitus/hospitus/pkg/provider"
)

// --- Jobs ---

func TestListJobs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		json.NewEncoder(w).Encode(JobListResponse{Jobs: []*job.Job{}, Count: 0})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	res, err := c.ListJobs(context.Background(), "")
	if err != nil || res == nil {
		t.Fatalf("ListJobs: err=%v", err)
	}
}

func TestListJobs_WithStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "status=running" {
			t.Errorf("expected status=running query, got %s", r.URL.RawQuery)
		}
		json.NewEncoder(w).Encode(JobListResponse{Jobs: []*job.Job{}, Count: 0})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	res, err := c.ListJobs(context.Background(), "running")
	if err != nil || res == nil {
		t.Fatalf("ListJobs with status: err=%v", err)
	}
}

func TestGetJob(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(job.Job{ID: "abc"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	j, err := c.GetJob(context.Background(), "abc")
	if err != nil || j == nil {
		t.Fatalf("GetJob: err=%v", err)
	}
}

func TestCancelJob(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.CancelJob(context.Background(), "abc"); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
}

func TestDeleteJob(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.DeleteJob(context.Background(), "abc"); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
}

func TestJobStats(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]int{"running": 2, "completed": 5})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	stats, err := c.JobStats(context.Background())
	if err != nil || stats == nil {
		t.Fatalf("JobStats: err=%v", err)
	}
	if stats["running"] != 2 {
		t.Errorf("expected running=2, got %d", stats["running"])
	}
}

// --- Monitoring ---

func TestGetInstanceMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(InstanceMetrics{CPUUsagePercent: 12.5})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	m, err := c.GetInstanceMetrics(context.Background(), "myjail")
	if err != nil || m == nil {
		t.Fatalf("GetInstanceMetrics: err=%v", err)
	}
	if m.CPUUsagePercent != 12.5 {
		t.Errorf("unexpected CPU: %f", m.CPUUsagePercent)
	}
}

func TestGetInstanceHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(InstanceHealth{Status: "healthy"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	h, err := c.GetInstanceHealth(context.Background(), "myjail")
	if err != nil || h == nil {
		t.Fatalf("GetInstanceHealth: err=%v", err)
	}
	if h.Status != "healthy" {
		t.Errorf("unexpected status: %q", h.Status)
	}
}

// --- Media ---

func TestInsertMedia(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.InsertMedia(context.Background(), "myjail", provider.MediaSpec{Path: "/iso/ubuntu.iso"}); err != nil {
		t.Fatalf("InsertMedia: %v", err)
	}
}

func TestEjectMedia(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.EjectMedia(context.Background(), "myjail", "cdrom0"); err != nil {
		t.Fatalf("EjectMedia: %v", err)
	}
}

func TestListMedia(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"media": []provider.MediaInfo{{DeviceID: "cdrom0", Path: "/iso/ubuntu.iso"}},
		})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	media, err := c.ListMedia(context.Background(), "myjail")
	if err != nil {
		t.Fatalf("ListMedia: %v", err)
	}
	if len(media) != 1 {
		t.Errorf("expected 1 media item, got %d", len(media))
	}
}

func TestSetBootOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.SetBootOrder(context.Background(), "myjail", provider.BootOrder{Devices: []provider.BootDevice{provider.BootDeviceHardDisk}}); err != nil {
		t.Fatalf("SetBootOrder: %v", err)
	}
}

func TestGetBootOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(provider.BootOrder{Devices: []provider.BootDevice{provider.BootDeviceHardDisk}})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	bo, err := c.GetBootOrder(context.Background(), "myjail")
	if err != nil || bo == nil {
		t.Fatalf("GetBootOrder: err=%v", err)
	}
	if len(bo.Devices) != 1 || bo.Devices[0] != provider.BootDeviceHardDisk {
		t.Errorf("unexpected boot order: %v", bo.Devices)
	}
}

// --- Volumes ---

func TestCreateVolume(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(VolumeInfo{Name: "mydata"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	v, err := c.CreateVolume(context.Background(), CreateVolumeRequest{Name: "mydata", Size: "10G"})
	if err != nil || v == nil {
		t.Fatalf("CreateVolume: err=%v", err)
	}
}

func TestDeleteVolume(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.DeleteVolume(context.Background(), "mydata"); err != nil {
		t.Fatalf("DeleteVolume: %v", err)
	}
}

func TestListVolumes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]VolumeInfo{{Name: "v1"}, {Name: "v2"}})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	vols, err := c.ListVolumes(context.Background())
	if err != nil {
		t.Fatalf("ListVolumes: %v", err)
	}
	if len(vols) != 2 {
		t.Errorf("expected 2 volumes, got %d", len(vols))
	}
}

func TestGetVolume(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(VolumeInfo{Name: "v1", Quota: 10737418240})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	v, err := c.GetVolume(context.Background(), "v1")
	if err != nil || v == nil {
		t.Fatalf("GetVolume: err=%v", err)
	}
}

func TestAttachVolume(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.AttachVolume(context.Background(), "myjail", "v1", "/mnt/data"); err != nil {
		t.Fatalf("AttachVolume: %v", err)
	}
}

func TestDetachVolume(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.DetachVolume(context.Background(), "myjail", "v1"); err != nil {
		t.Fatalf("DetachVolume: %v", err)
	}
}

// --- Services ---

func TestServiceAction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ServiceActionResult{Success: true})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	res, err := c.ServiceAction(context.Background(), "myjail", ServiceActionRequest{
		ServiceName: "nginx",
		Action:      "start",
	})
	if err != nil || res == nil || !res.Success {
		t.Fatalf("ServiceAction: err=%v res=%v", err, res)
	}
}

func TestGetServiceStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ServiceStatus{Name: "nginx", Running: true})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	s, err := c.GetServiceStatus(context.Background(), "myjail", "nginx")
	if err != nil || s == nil {
		t.Fatalf("GetServiceStatus: err=%v", err)
	}
	if !s.Running {
		t.Error("expected service to be running")
	}
}

func TestListServices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]ServiceInfo{{Name: "nginx"}, {Name: "sshd"}})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	svc, err := c.ListServices(context.Background(), "myjail", "")
	if err != nil {
		t.Fatalf("ListServices: %v", err)
	}
	if len(svc) != 2 {
		t.Errorf("expected 2 services, got %d", len(svc))
	}
}

func TestListServices_WithFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery == "" {
			t.Error("expected filter query param")
		}
		json.NewEncoder(w).Encode([]ServiceInfo{{Name: "nginx"}})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	_, err := c.ListServices(context.Background(), "myjail", "nginx")
	if err != nil {
		t.Fatalf("ListServices with filter: %v", err)
	}
}

// --- ResourceLimits ---

func TestGetResourceLimits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"limits": []ResourceLimit{{Resource: "cpu", Action: "deny", Amount: "4"}},
		})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	limits, err := c.GetResourceLimits(context.Background(), "myjail")
	if err != nil {
		t.Fatalf("GetResourceLimits: %v", err)
	}
	if len(limits) != 1 {
		t.Errorf("expected 1 limit, got %d", len(limits))
	}
}

func TestSetResourceLimits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	limits := []ResourceLimit{{Resource: "maxproc", Action: "deny", Amount: "100"}}
	if err := c.SetResourceLimits(context.Background(), "myjail", limits); err != nil {
		t.Fatalf("SetResourceLimits: %v", err)
	}
}

func TestRemoveResourceLimits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.RemoveResourceLimits(context.Background(), "myjail"); err != nil {
		t.Fatalf("RemoveResourceLimits: %v", err)
	}
}

// --- Networking ---

func TestGetVNETStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"vnet": VNETConfig{Enabled: true, Bridge: "hospitus0"},
		})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	v, err := c.GetVNETStatus(context.Background(), "myjail")
	if err != nil || v == nil {
		t.Fatalf("GetVNETStatus: err=%v", err)
	}
	if !v.Enabled {
		t.Error("expected VNET enabled")
	}
}

func TestEnableVNET(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.EnableVNET(context.Background(), "myjail", VNETConfig{Enabled: true}); err != nil {
		t.Fatalf("EnableVNET: %v", err)
	}
}

func TestDisableVNET(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.DisableVNET(context.Background(), "myjail"); err != nil {
		t.Fatalf("DisableVNET: %v", err)
	}
}

func TestExposePort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(PortMapping{ID: "pm1", HostPort: 8080, TargetPort: 80})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	res, err := c.ExposePort(context.Background(), "myjail", ExposePortRequest{HostPort: 8080, TargetPort: 80})
	if err != nil || res == nil {
		t.Fatalf("ExposePort: err=%v", err)
	}
	if res.HostPort != 8080 {
		t.Errorf("expected HostPort=8080, got %d", res.HostPort)
	}
}

func TestUnexposePort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.UnexposePort(context.Background(), "myjail", 8080, "tcp"); err != nil {
		t.Fatalf("UnexposePort: %v", err)
	}
}

func TestListExposedPorts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]PortMapping{{HostPort: 8080}})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	ports, err := c.ListExposedPorts(context.Background(), "myjail")
	if err != nil {
		t.Fatalf("ListExposedPorts: %v", err)
	}
	if len(ports) != 1 {
		t.Errorf("expected 1 port mapping, got %d", len(ports))
	}
}

func TestAddNetworkInterface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(NetworkInterfaceInfo{Name: "epair0b", Bridge: "hospitus0"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	iface, err := c.AddNetworkInterface(context.Background(), "myjail", NetworkInterfaceRequest{Bridge: "hospitus0"})
	if err != nil || iface == nil {
		t.Fatalf("AddNetworkInterface: err=%v", err)
	}
}

func TestRemoveNetworkInterface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.RemoveNetworkInterface(context.Background(), "myjail", "epair0b"); err != nil {
		t.Fatalf("RemoveNetworkInterface: %v", err)
	}
}

func TestListNetworkInterfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]NetworkInterfaceInfo{{Name: "epair0b"}})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	ifaces, err := c.ListNetworkInterfaces(context.Background(), "myjail")
	if err != nil {
		t.Fatalf("ListNetworkInterfaces: %v", err)
	}
	if len(ifaces) != 1 {
		t.Errorf("expected 1 interface, got %d", len(ifaces))
	}
}

// --- Snapshots ---

func TestListSnapshots(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"snapshots": []SnapshotInfo{{Name: "snap1"}, {Name: "snap2"}},
		})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	snaps, err := c.ListSnapshots(context.Background(), "myjail")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 2 {
		t.Errorf("expected 2 snapshots, got %d", len(snaps))
	}
}

func TestCreateSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.CreateSnapshot(context.Background(), "myjail", "snap1"); err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
}

func TestDeleteSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.DeleteSnapshot(context.Background(), "myjail", "snap1"); err != nil {
		t.Fatalf("DeleteSnapshot: %v", err)
	}
}

func TestRestoreSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.RestoreSnapshot(context.Background(), "myjail", "snap1"); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
}

func TestSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.Snapshot(context.Background(), "myjail"); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
}

func TestCloneInstance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(CloneResult{CloneInstance: "myjail-clone"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	res, err := c.CloneInstance(context.Background(), "myjail", CloneOptions{Name: "myjail-clone"})
	if err != nil || res == nil {
		t.Fatalf("CloneInstance: err=%v", err)
	}
}

func TestCloneFromSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(CloneResult{CloneInstance: "myjail-clone"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	res, err := c.CloneFromSnapshot(context.Background(), "myjail", "snap1", CloneOptions{Name: "myjail-clone"})
	if err != nil || res == nil {
		t.Fatalf("CloneFromSnapshot: err=%v", err)
	}
}

// TestListCheckpoints serves the shape the daemon actually sends: the list is
// wrapped in {"instance", "checkpoints", "count"}, not a bare array.
func TestListCheckpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"instance":    "myvm",
			"checkpoints": []CheckpointInfo{{Name: "cp1"}},
			"count":       1,
		})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	cps, err := c.ListCheckpoints(context.Background(), "myvm")
	if err != nil {
		t.Fatalf("ListCheckpoints: %v", err)
	}
	if len(cps) != 1 {
		t.Fatalf("expected 1 checkpoint, got %d", len(cps))
	}
	if cps[0].Name != "cp1" {
		t.Errorf("checkpoint name = %q, want cp1", cps[0].Name)
	}
}

// --- Exec ---

func TestExecCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ExecResult{ExitCode: 0, Stdout: "hello"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	res, err := c.ExecCommand(context.Background(), "myjail", ExecRequest{Command: "echo", Args: []string{"hello"}})
	if err != nil || res == nil {
		t.Fatalf("ExecCommand: err=%v", err)
	}
	if res.Stdout != "hello" {
		t.Errorf("unexpected stdout: %q", res.Stdout)
	}
}

func TestExecCommandStream(t *testing.T) {
	t.Run("stdout and exit code", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("line one\nline two\nEXIT_CODE: 42\n"))
		}))
		defer srv.Close()
		c := NewClient(srv.URL)
		var stdout, stderr stdioWriter
		code, err := c.ExecCommandStream(context.Background(), "myjail", ExecRequest{Command: "ls"}, &stdout, &stderr)
		if err != nil {
			t.Fatalf("ExecCommandStream: %v", err)
		}
		if code != 42 {
			t.Errorf("expected exit code 42, got %d", code)
		}
		if got := string(stdout.buf); got != "line one\nline two\n" {
			t.Errorf("unexpected stdout: %q", got)
		}
		if got := string(stderr.buf); got != "" {
			t.Errorf("expected empty stderr, got %q", got)
		}
	})

	t.Run("stderr from ERROR lines", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ERROR: something failed\nEXIT_CODE: 1\n"))
		}))
		defer srv.Close()
		c := NewClient(srv.URL)
		var stdout, stderr stdioWriter
		code, err := c.ExecCommandStream(context.Background(), "myjail", ExecRequest{Command: "ls"}, &stdout, &stderr)
		if err != nil {
			t.Fatalf("ExecCommandStream: %v", err)
		}
		if code != 1 {
			t.Errorf("expected exit code 1, got %d", code)
		}
		if got := string(stderr.buf); got != "something failed\n" {
			t.Errorf("unexpected stderr: %q", got)
		}
		if got := string(stdout.buf); got != "" {
			t.Errorf("expected empty stdout, got %q", got)
		}
	})
}

// stdioWriter is a simple io.Writer for tests
type stdioWriter struct {
	buf []byte
}

func (w *stdioWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	return len(p), nil
}

// --- Export/Import ---

func TestExportInstance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ExportResult{Instance: "myjail", ExportPath: "/tmp/myjail.tar.gz"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	res, err := c.ExportInstance(context.Background(), "myjail", ExportOptions{ExportPath: "/tmp/myjail.tar.gz"})
	if err != nil || res == nil {
		t.Fatalf("ExportInstance: err=%v", err)
	}
}

func TestImportInstance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ImportResult{Message: "imported"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	res, err := c.ImportInstance(context.Background(), ImportOptions{ImportPath: "/tmp/myjail.tar.gz", Provider: "jail"})
	if err != nil || res == nil {
		t.Fatalf("ImportInstance: err=%v", err)
	}
}
