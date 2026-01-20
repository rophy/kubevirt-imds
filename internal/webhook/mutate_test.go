package webhook

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestShouldMutate(t *testing.T) {
	mutator := NewMutator(Config{IMDSImage: "test-image:latest"})

	tests := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{
			name: "pod with IMDS enabled annotation and kubevirt label",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationEnabled: "true",
					},
					Labels: map[string]string{
						"kubevirt.io/domain": "test-vm",
					},
				},
			},
			want: true,
		},
		{
			name: "pod without annotations",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"kubevirt.io/domain": "test-vm",
					},
				},
			},
			want: false,
		},
		{
			name: "pod with IMDS disabled",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationEnabled: "false",
					},
					Labels: map[string]string{
						"kubevirt.io/domain": "test-vm",
					},
				},
			},
			want: false,
		},
		{
			name: "pod with IMDS enabled but no kubevirt label",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationEnabled: "true",
					},
					Labels: map[string]string{
						"app": "other",
					},
				},
			},
			want: false,
		},
		{
			name: "pod with IMDS enabled but no labels",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationEnabled: "true",
					},
				},
			},
			want: false,
		},
		{
			name: "pod already injected",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationEnabled:  "true",
						AnnotationInjected: "true",
					},
					Labels: map[string]string{
						"kubevirt.io/domain": "test-vm",
					},
				},
			},
			want: false,
		},
		{
			name: "pod with wrong annotation value",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationEnabled: "yes", // should be "true"
					},
					Labels: map[string]string{
						"kubevirt.io/domain": "test-vm",
					},
				},
			},
			want: false,
		},
		{
			name: "empty pod",
			pod:  &corev1.Pod{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mutator.ShouldMutate(tt.pod)
			if got != tt.want {
				t.Errorf("ShouldMutate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMutate(t *testing.T) {
	mutator := NewMutator(Config{
		IMDSImage:       "test-image:latest",
		ImagePullPolicy: corev1.PullIfNotPresent,
	})

	tests := []struct {
		name       string
		pod        *corev1.Pod
		wantErr    bool
		checkPatch func(t *testing.T, patches []PatchOperation)
	}{
		{
			name: "basic mutation adds volume and container",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "test-ns",
					Name:      "test-pod",
					Labels: map[string]string{
						"kubevirt.io/domain": "test-vm",
					},
					Annotations: map[string]string{
						AnnotationEnabled: "true",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "compute"},
					},
					Volumes: []corev1.Volume{
						{Name: "existing-volume"},
					},
				},
			},
			wantErr: false,
			checkPatch: func(t *testing.T, patches []PatchOperation) {
				if len(patches) != 3 {
					t.Errorf("expected 3 patches, got %d", len(patches))
					return
				}

				// Check volume patch
				if patches[0].Op != "add" || patches[0].Path != "/spec/volumes/-" {
					t.Errorf("patch[0] = %+v, want add volume", patches[0])
				}

				// Check container patch
				if patches[1].Op != "add" || patches[1].Path != "/spec/containers/-" {
					t.Errorf("patch[1] = %+v, want add container", patches[1])
				}

				// Check annotation patch
				if patches[2].Op != "add" {
					t.Errorf("patch[2] = %+v, want add annotation", patches[2])
				}
			},
		},
		{
			name: "mutation with empty volumes creates volumes array",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "test-ns",
					Name:      "test-pod",
					Labels: map[string]string{
						"kubevirt.io/domain": "test-vm",
					},
					Annotations: map[string]string{
						AnnotationEnabled: "true",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "compute"},
					},
				},
			},
			wantErr: false,
			checkPatch: func(t *testing.T, patches []PatchOperation) {
				// First patch should create the volumes array
				if patches[0].Op != "add" || patches[0].Path != "/spec/volumes" {
					t.Errorf("patch[0] = %+v, want add volumes array", patches[0])
				}
			},
		},
		{
			name: "mutation with bridge name override",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "test-ns",
					Name:      "test-pod",
					Labels: map[string]string{
						"kubevirt.io/domain": "test-vm",
					},
					Annotations: map[string]string{
						AnnotationEnabled:    "true",
						AnnotationBridgeName: "custom-bridge",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "compute"},
					},
					Volumes: []corev1.Volume{},
				},
			},
			wantErr: false,
			checkPatch: func(t *testing.T, patches []PatchOperation) {
				// Find container patch and check for bridge env var
				for _, patch := range patches {
					if patch.Path == "/spec/containers/-" {
						container, ok := patch.Value.(corev1.Container)
						if !ok {
							t.Error("container patch value is not a Container")
							return
						}
						found := false
						for _, env := range container.Env {
							if env.Name == "IMDS_BRIDGE_NAME" && env.Value == "custom-bridge" {
								found = true
								break
							}
						}
						if !found {
							t.Error("expected IMDS_BRIDGE_NAME env var with value 'custom-bridge'")
						}
						return
					}
				}
				t.Error("container patch not found")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patches, err := mutator.Mutate(tt.pod)
			if tt.wantErr {
				if err == nil {
					t.Error("Mutate() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("Mutate() unexpected error: %v", err)
				return
			}
			if tt.checkPatch != nil {
				tt.checkPatch(t, patches)
			}
		})
	}
}

func TestEscapeJSONPointer(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no special characters",
			input: "simple-key",
			want:  "simple-key",
		},
		{
			name:  "with tilde",
			input: "key~with~tilde",
			want:  "key~0with~0tilde",
		},
		{
			name:  "with slash",
			input: "key/with/slash",
			want:  "key~1with~1slash",
		},
		{
			name:  "with both tilde and slash",
			input: "key~with/both",
			want:  "key~0with~1both",
		},
		{
			name:  "annotation with dots and slashes",
			input: "imds.kubevirt.io/enabled",
			want:  "imds.kubevirt.io~1enabled",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "only tilde",
			input: "~",
			want:  "~0",
		},
		{
			name:  "only slash",
			input: "/",
			want:  "~1",
		},
		{
			name:  "tilde followed by slash",
			input: "~/",
			want:  "~0~1",
		},
		{
			name:  "multiple consecutive tildes",
			input: "~~~",
			want:  "~0~0~0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := escapeJSONPointer(tt.input)
			if got != tt.want {
				t.Errorf("escapeJSONPointer(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCreatePatch(t *testing.T) {
	tests := []struct {
		name    string
		patches []PatchOperation
		wantErr bool
	}{
		{
			name: "valid patches",
			patches: []PatchOperation{
				{Op: "add", Path: "/spec/containers/-", Value: "test"},
			},
			wantErr: false,
		},
		{
			name:    "empty patches",
			patches: []PatchOperation{},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CreatePatch(tt.patches)
			if tt.wantErr {
				if err == nil {
					t.Error("CreatePatch() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("CreatePatch() unexpected error: %v", err)
				return
			}

			// Verify it's valid JSON
			var parsed []map[string]interface{}
			if err := json.Unmarshal(got, &parsed); err != nil {
				t.Errorf("CreatePatch() produced invalid JSON: %v", err)
			}
		})
	}
}

func TestCreateServerContainer(t *testing.T) {
	mutator := NewMutator(Config{
		IMDSImage:       "test-image:v1.0",
		ImagePullPolicy: corev1.PullAlways,
	})

	container := mutator.createServerContainer("test-ns", "test-vm", "")

	// Check container name
	if container.Name != ContainerName {
		t.Errorf("container.Name = %q, want %q", container.Name, ContainerName)
	}

	// Check image
	if container.Image != "test-image:v1.0" {
		t.Errorf("container.Image = %q, want %q", container.Image, "test-image:v1.0")
	}

	// Check image pull policy
	if container.ImagePullPolicy != corev1.PullAlways {
		t.Errorf("container.ImagePullPolicy = %v, want %v", container.ImagePullPolicy, corev1.PullAlways)
	}

	// Check command
	if len(container.Command) != 2 || container.Command[0] != "/imds-server" || container.Command[1] != "run" {
		t.Errorf("container.Command = %v, want [/imds-server run]", container.Command)
	}

	// Check security context
	if container.SecurityContext == nil {
		t.Fatal("container.SecurityContext is nil")
	}
	if container.SecurityContext.RunAsUser == nil || *container.SecurityContext.RunAsUser != 0 {
		t.Error("container should run as root (user 0)")
	}
	if container.SecurityContext.RunAsNonRoot == nil || *container.SecurityContext.RunAsNonRoot != false {
		t.Error("container.SecurityContext.RunAsNonRoot should be false")
	}
	if container.SecurityContext.Capabilities == nil {
		t.Fatal("container.SecurityContext.Capabilities is nil")
	}
	hasNetAdmin := false
	for _, cap := range container.SecurityContext.Capabilities.Add {
		if cap == "NET_ADMIN" {
			hasNetAdmin = true
			break
		}
	}
	if !hasNetAdmin {
		t.Error("container should have NET_ADMIN capability")
	}

	// Check volume mounts
	if len(container.VolumeMounts) != 1 {
		t.Errorf("expected 1 volume mount, got %d", len(container.VolumeMounts))
	}
	if container.VolumeMounts[0].Name != TokenVolumeName {
		t.Errorf("volume mount name = %q, want %q", container.VolumeMounts[0].Name, TokenVolumeName)
	}

	// Check required env vars
	envMap := make(map[string]string)
	for _, env := range container.Env {
		envMap[env.Name] = env.Value
	}
	if envMap["IMDS_NAMESPACE"] != "test-ns" {
		t.Errorf("IMDS_NAMESPACE = %q, want %q", envMap["IMDS_NAMESPACE"], "test-ns")
	}
	if envMap["IMDS_VM_NAME"] != "test-vm" {
		t.Errorf("IMDS_VM_NAME = %q, want %q", envMap["IMDS_VM_NAME"], "test-vm")
	}
}

func TestCreateTokenVolume(t *testing.T) {
	mutator := NewMutator(Config{IMDSImage: "test-image:latest"})
	volume := mutator.createTokenVolume()

	// Check volume name
	if volume.Name != TokenVolumeName {
		t.Errorf("volume.Name = %q, want %q", volume.Name, TokenVolumeName)
	}

	// Check projected volume source
	if volume.Projected == nil {
		t.Fatal("volume.Projected is nil")
	}
	if len(volume.Projected.Sources) != 1 {
		t.Fatalf("expected 1 projected source, got %d", len(volume.Projected.Sources))
	}

	// Check service account token projection
	tokenSource := volume.Projected.Sources[0].ServiceAccountToken
	if tokenSource == nil {
		t.Fatal("ServiceAccountToken projection is nil")
	}
	if tokenSource.Path != "token" {
		t.Errorf("token path = %q, want %q", tokenSource.Path, "token")
	}
	if tokenSource.ExpirationSeconds == nil || *tokenSource.ExpirationSeconds != DefaultTokenExpiration {
		t.Errorf("token expiration = %v, want %d", tokenSource.ExpirationSeconds, DefaultTokenExpiration)
	}
}
