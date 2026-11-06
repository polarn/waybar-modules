//go:build linux

package main

import (
	"slices"
	"testing"
)

const pwDump = `[
  {"id": 0, "type": "PipeWire:Interface:Core", "info": {"props": {}}},
  {"id": 40, "type": "PipeWire:Interface:Client", "info": {"props": {
    "application.name": "Firefox", "application.process.binary": "firefox", "application.process.id": 4211}}},
  {"id": 78, "type": "PipeWire:Interface:Node", "info": {"state": "running", "props": {
    "media.class": "Video/Source", "api.v4l2.path": "/dev/video0", "node.name": "v4l2_input.webcam"}}},
  {"id": 79, "type": "PipeWire:Interface:Node", "info": {"state": "running", "props": {
    "media.class": "Video/Source", "node.name": "xdph-streaming-0"}}},
  {"id": 90, "type": "PipeWire:Interface:Node", "info": {"state": "running", "props": {
    "media.class": "Stream/Input/Video", "client.id": 40, "node.name": "webrtc-consume-stream"}}},
  {"id": 91, "type": "PipeWire:Interface:Node", "info": {"state": "running", "props": {
    "media.class": "Stream/Input/Video", "node.name": "screen-consumer"}}},
  {"id": 100, "type": "PipeWire:Interface:Link", "info": {"output-node-id": 78, "input-node-id": 90}},
  {"id": 101, "type": "PipeWire:Interface:Link", "info": {"output-node-id": 79, "input-node-id": 91}},
  {"id": 102, "type": "PipeWire:Interface:Node", "info": null}
]`

func TestParsePipewire(t *testing.T) {
	devs := map[string]string{"/dev/video0": "HD 720P webcam", "/dev/video1": "HD 720P webcam"}
	got, err := parsePipewire([]byte(pwDump), devs)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"/dev/video0": {"Firefox (firefox, pid 4211)"}}
	if len(got) != len(want) || !slices.Equal(got["/dev/video0"], want["/dev/video0"]) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParsePipewireIdleNode(t *testing.T) {
	devs := map[string]string{"/dev/video0": "HD 720P webcam"}
	data := `[{"id": 78, "type": "PipeWire:Interface:Node", "info": {"state": "idle", "props": {
	  "media.class": "Video/Source", "api.v4l2.path": "/dev/video0"}}}]`
	got, err := parsePipewire([]byte(data), devs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("idle node reported as in use: %v", got)
	}
}

func TestRender(t *testing.T) {
	if w, _ := render(map[string]string{}, nil); w.Class != "absent" || w.Text != iconCameraOff {
		t.Errorf("no devices: %+v", w)
	}
	devs := map[string]string{"/dev/video0": "Cam & Co", "/dev/video1": "Cam & Co"}
	if w, via := render(devs, nil); w.Class != "idle" || via || w.ToolTip != "<b>Cam &amp; Co</b>\nidle" {
		t.Errorf("idle: %+v", w)
	}
	held := []holder{{pid: 7, comm: "chrome", dev: "/dev/video0"}, {pid: 7, comm: "chrome", dev: "/dev/video1"}}
	if w, via := render(devs, held); w.Class != "active" || via || w.ToolTip != "<b>Cam &amp; Co</b>\nchrome (pid 7)" {
		t.Errorf("active: %+v", w)
	}
}
