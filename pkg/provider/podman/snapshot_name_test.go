package podman

import "testing"

// TestSnapshotNameFromLabels covers what identifies a snapshot now: the labels
// stamped on the image at commit time. Decoding the image name instead was
// ambiguous whenever a container name contained a dash — "web" claimed
// "web-2"'s snapshot as its own "2-s1", and deleting that phantom removed
// web-2's real image.
func TestSnapshotNameFromLabels(t *testing.T) {
	tests := []struct {
		name      string
		labels    map[string]string
		container string
		want      string
		wantOK    bool
	}{
		{
			name: "this container's snapshot",
			labels: map[string]string{
				snapshotContainerLabel: "web",
				snapshotNameLabel:      "before-upgrade",
			},
			container: "web",
			want:      "before-upgrade",
			wantOK:    true,
		},
		{
			name: "another container's snapshot",
			labels: map[string]string{
				snapshotContainerLabel: "db",
				snapshotNameLabel:      "s1",
			},
			container: "web",
			wantOK:    false,
		},
		{
			// The ambiguity the labels exist to remove: this image's name is
			// "hospitus-snapshot-web-2-s1", which reads as web's snapshot "2-s1".
			name: "container whose name shares a dash prefix",
			labels: map[string]string{
				snapshotContainerLabel: "web-2",
				snapshotNameLabel:      "s1",
			},
			container: "web",
			wantOK:    false,
		},
		{
			// …and read for its real owner, it decodes unambiguously.
			name: "same image, its real owner",
			labels: map[string]string{
				snapshotContainerLabel: "web-2",
				snapshotNameLabel:      "s1",
			},
			container: "web-2",
			want:      "s1",
			wantOK:    true,
		},
		{
			name: "a snapshot name containing dashes",
			labels: map[string]string{
				snapshotContainerLabel: "web",
				snapshotNameLabel:      "2-s1",
			},
			container: "web",
			want:      "2-s1",
			wantOK:    true,
		},
		{
			name:      "not a snapshot at all",
			labels:    map[string]string{"maintainer": "someone"},
			container: "web",
			wantOK:    false,
		},
		{
			name:      "no labels (committed before snapshots were labeled)",
			labels:    nil,
			container: "web",
			wantOK:    false,
		},
		{
			name:      "container label without a name label",
			labels:    map[string]string{snapshotContainerLabel: "web"},
			container: "web",
			wantOK:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := snapshotNameFromLabels(tt.labels, tt.container)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Errorf("name = %q, want %q", got, tt.want)
			}
		})
	}
}
