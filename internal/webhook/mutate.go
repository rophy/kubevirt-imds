package webhook

import (
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

const (
	// AnnotationEnabled is the annotation to enable IMDS injection
	AnnotationEnabled = "imds.kubevirt.io/enabled"
	// AnnotationBridgeName is the annotation to override bridge name
	AnnotationBridgeName = "imds.kubevirt.io/bridge-name"
	// AnnotationInjected marks that IMDS has been injected
	AnnotationInjected = "imds.kubevirt.io/injected"

	// Container and volume names
	ContainerName   = "imds-server"
	TokenVolumeName = "imds-token"

	// Default values
	DefaultTokenPath       = "/var/run/secrets/tokens/token"
	DefaultTokenExpiration = int64(3600)
)

// Config holds the webhook configuration
type Config struct {
	// IMDSImage is the image to use for the IMDS sidecar
	IMDSImage string
	// ImagePullPolicy is the pull policy for the IMDS image
	ImagePullPolicy corev1.PullPolicy
}

// Mutator handles pod mutation for IMDS injection
type Mutator struct {
	config Config
}

// NewMutator creates a new Mutator with the given configuration
func NewMutator(config Config) *Mutator {
	if config.ImagePullPolicy == "" {
		config.ImagePullPolicy = corev1.PullIfNotPresent
	}
	return &Mutator{config: config}
}

// ShouldMutate checks if the pod should be mutated
func (m *Mutator) ShouldMutate(pod *corev1.Pod) bool {
	// Check if IMDS is enabled via annotation
	if pod.Annotations == nil {
		return false
	}

	enabled, ok := pod.Annotations[AnnotationEnabled]
	if !ok || enabled != "true" {
		return false
	}

	// Check if already injected
	if pod.Annotations[AnnotationInjected] == "true" {
		return false
	}

	// Check if this is a virt-launcher pod (has kubevirt.io/domain label)
	if pod.Labels == nil {
		return false
	}
	if _, ok := pod.Labels["kubevirt.io/domain"]; !ok {
		return false
	}

	return true
}

// Mutate mutates the pod to inject IMDS sidecar
func (m *Mutator) Mutate(pod *corev1.Pod) ([]PatchOperation, error) {
	var patches []PatchOperation

	// Get VM name from label
	vmName := pod.Labels["kubevirt.io/domain"]

	// Get bridge name override if specified
	bridgeName := ""
	if pod.Annotations != nil {
		bridgeName = pod.Annotations[AnnotationBridgeName]
	}

	// Add projected ServiceAccount token volume
	tokenVolume := m.createTokenVolume()
	patches = append(patches, addVolume(pod, tokenVolume))

	// Add IMDS server container (runs init then serve in sequence)
	// We don't use an init container because the VM bridge (k6t-*) is created
	// by the compute container, which runs after init containers.
	serverContainer := m.createServerContainer(pod.Namespace, vmName, bridgeName)
	patches = append(patches, addContainer(pod, serverContainer))

	// Add injected annotation
	patches = append(patches, addAnnotation(pod, AnnotationInjected, "true"))

	return patches, nil
}

// createTokenVolume creates the projected ServiceAccount token volume
func (m *Mutator) createTokenVolume() corev1.Volume {
	expiration := DefaultTokenExpiration
	return corev1.Volume{
		Name: TokenVolumeName,
		VolumeSource: corev1.VolumeSource{
			Projected: &corev1.ProjectedVolumeSource{
				Sources: []corev1.VolumeProjection{
					{
						ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
							Path:              "token",
							ExpirationSeconds: &expiration,
						},
					},
				},
			},
		},
	}
}

// createServerContainer creates the IMDS server container
// The container runs "run" command which waits for the bridge, sets up veth, then serves HTTP.
func (m *Mutator) createServerContainer(namespace, vmName, bridgeName string) corev1.Container {
	env := []corev1.EnvVar{
		{Name: "IMDS_TOKEN_PATH", Value: DefaultTokenPath},
		{Name: "IMDS_NAMESPACE", Value: namespace},
		{Name: "IMDS_VM_NAME", Value: vmName},
		{
			Name: "IMDS_SA_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "spec.serviceAccountName",
				},
			},
		},
	}

	if bridgeName != "" {
		env = append(env, corev1.EnvVar{Name: "IMDS_BRIDGE_NAME", Value: bridgeName})
	}

	// Override pod-level security context to allow NET_ADMIN to work.
	// virt-launcher pods enforce runAsNonRoot: true and runAsUser: 107,
	// but NET_ADMIN requires root to create veth pairs.
	runAsNonRoot := false
	runAsUser := int64(0)

	return corev1.Container{
		Name:            ContainerName,
		Image:           m.config.IMDSImage,
		ImagePullPolicy: m.config.ImagePullPolicy,
		Command:         []string{"/imds-server", "run"},
		Env:             env,
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot: &runAsNonRoot,
			RunAsUser:    &runAsUser,
			Capabilities: &corev1.Capabilities{
				Add: []corev1.Capability{"NET_ADMIN"},
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      TokenVolumeName,
				MountPath: "/var/run/secrets/tokens",
				ReadOnly:  true,
			},
		},
	}
}

// PatchOperation represents a JSON patch operation
type PatchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

// addVolume creates a patch to add a volume
func addVolume(pod *corev1.Pod, volume corev1.Volume) PatchOperation {
	if len(pod.Spec.Volumes) == 0 {
		return PatchOperation{
			Op:    "add",
			Path:  "/spec/volumes",
			Value: []corev1.Volume{volume},
		}
	}
	return PatchOperation{
		Op:    "add",
		Path:  "/spec/volumes/-",
		Value: volume,
	}
}

// addContainer creates a patch to add a container
func addContainer(pod *corev1.Pod, container corev1.Container) PatchOperation {
	return PatchOperation{
		Op:    "add",
		Path:  "/spec/containers/-",
		Value: container,
	}
}

// addAnnotation creates a patch to add an annotation
func addAnnotation(pod *corev1.Pod, key, value string) PatchOperation {
	if pod.Annotations == nil {
		return PatchOperation{
			Op:    "add",
			Path:  "/metadata/annotations",
			Value: map[string]string{key: value},
		}
	}
	// Escape special characters in annotation key for JSON pointer
	escapedKey := escapeJSONPointer(key)
	return PatchOperation{
		Op:    "add",
		Path:  fmt.Sprintf("/metadata/annotations/%s", escapedKey),
		Value: value,
	}
}

// escapeJSONPointer escapes special characters for JSON pointer (RFC 6901)
func escapeJSONPointer(s string) string {
	s = replaceAll(s, "~", "~0")
	s = replaceAll(s, "/", "~1")
	return s
}

func replaceAll(s, old, new string) string {
	result := ""
	for i := 0; i < len(s); i++ {
		if i+len(old) <= len(s) && s[i:i+len(old)] == old {
			result += new
			i += len(old) - 1
		} else {
			result += string(s[i])
		}
	}
	return result
}

// CreatePatch creates a JSON patch from patch operations
func CreatePatch(patches []PatchOperation) ([]byte, error) {
	return json.Marshal(patches)
}
