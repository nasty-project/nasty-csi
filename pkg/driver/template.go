// Package driver provides volume name templating functionality for CSI volumes.
package driver

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"k8s.io/klog/v2"
)

// CSI parameter keys for PVC/PV information passed by Kubernetes.
// These are standard CSI parameters that Kubernetes populates automatically.
const (
	// CSIPVCName is the key for the PVC name in CSI parameters.
	CSIPVCName = "csi.storage.k8s.io/pvc/name"
	// CSIPVCNamespace is the key for the PVC namespace in CSI parameters.
	CSIPVCNamespace = "csi.storage.k8s.io/pvc/namespace"
	// CSIPVName is the key for the PV name in CSI parameters.
	CSIPVName = "csi.storage.k8s.io/pv/name"
)

// StorageClass parameter keys for name templating.
const (
	// ParamNameTemplate is the StorageClass parameter for full name template.
	// Example: "{{ .PVCNamespace }}-{{ .PVCName }}".
	ParamNameTemplate = "nameTemplate"
	// ParamNamePrefix is the StorageClass parameter for simple prefix.
	// Example: "prod-".
	ParamNamePrefix = "namePrefix"
	// ParamNameSuffix is the StorageClass parameter for simple suffix.
	// Example: "-data".
	ParamNameSuffix = "nameSuffix"
	// ParamCommentTemplate is the StorageClass parameter for dataset comment template.
	// Example: "{{ .PVCNamespace }}/{{ .PVCName }}".
	ParamCommentTemplate = "commentTemplate"
)

// VolumeNameContext holds the context variables available for name templating.
// These values are extracted from CSI CreateVolumeRequest parameters.
type VolumeNameContext struct {
	// PVCName is the name of the PersistentVolumeClaim (if available).
	PVCName string
	// PVCNamespace is the namespace of the PersistentVolumeClaim (if available).
	PVCNamespace string
	// PVName is the name of the PersistentVolume (CSI volume name).
	// This is always available as it comes from req.GetName().
	PVName string
}

// nameTemplateConfig holds parsed template configuration from StorageClass parameters.
type nameTemplateConfig struct {
	// template is the parsed Go template (nil if no template specified)
	template *template.Template
	// prefix is a simple prefix to prepend (used if no template)
	prefix string
	// suffix is a simple suffix to append (used if no template)
	suffix string
}

// Sentinel errors for volume name validation.
var (
	// ErrVolumeNameEmpty is returned when the volume name is empty after processing.
	ErrVolumeNameEmpty = errors.New("volume name cannot be empty")
	// ErrVolumeNameInvalid is returned when the volume name contains invalid characters.
	ErrVolumeNameInvalid = errors.New("invalid volume name: must start with alphanumeric and contain only alphanumeric, hyphen, underscore, colon, or period")
)

// validNameRegex matches valid subvolume names.
// Names can contain alphanumeric characters, hyphens, underscores, colons, and periods.
// They cannot start with a hyphen.
var validNameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]*$`)

const (
	maxBackendNameBytes       = 63
	backendNameSuffixLength   = 8
	backendNameSuffixAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	backendNameHashDomain     = "nasty-csi/backend-name/v2\x00"
)

// parseNameTemplateConfig extracts name templating configuration from StorageClass parameters.
// Returns nil, nil if no templating is configured (use default naming).
//
//nolint:nilnil // nil, nil is the expected return when no templating is configured
func parseNameTemplateConfig(params map[string]string) (*nameTemplateConfig, error) {
	templateStr := params[ParamNameTemplate]
	prefix := params[ParamNamePrefix]
	suffix := params[ParamNameSuffix]

	// No templating configured - use default naming
	if templateStr == "" && prefix == "" && suffix == "" {
		return nil, nil
	}

	config := &nameTemplateConfig{
		prefix: prefix,
		suffix: suffix,
	}

	// Parse template if provided
	if templateStr != "" {
		tmpl, err := template.New("volumeName").Parse(templateStr)
		if err != nil {
			return nil, fmt.Errorf("invalid nameTemplate '%s': %w", templateStr, err)
		}
		config.template = tmpl
		klog.V(4).Infof("Parsed name template: %s", templateStr)
	}

	return config, nil
}

// extractVolumeNameContext extracts template context from CSI parameters.
// The pvName parameter should come from req.GetName() in CreateVolumeRequest.
func extractVolumeNameContext(params map[string]string, pvName string) VolumeNameContext {
	ctx := VolumeNameContext{
		PVName:       pvName,
		PVCName:      params[CSIPVCName],
		PVCNamespace: params[CSIPVCNamespace],
	}

	klog.V(5).Infof("Extracted volume name context: PVName=%s, PVCName=%s, PVCNamespace=%s",
		ctx.PVName, ctx.PVCName, ctx.PVCNamespace)

	return ctx
}

// renderVolumeName generates the final volume name using template configuration.
// If no templating is configured, returns the original pvName.
// The rendered name is sanitized to be valid for subvolume names.
func renderVolumeNameCandidate(config *nameTemplateConfig, ctx VolumeNameContext) (string, error) {
	var name string

	if config == nil {
		return ctx.PVName, nil
	}

	if config.template != nil {
		// Use full template
		var buf bytes.Buffer
		if err := config.template.Execute(&buf, ctx); err != nil {
			return "", fmt.Errorf("failed to execute name template: %w", err)
		}
		name = buf.String()
	} else {
		// Use simple prefix/suffix
		name = config.prefix + ctx.PVName + config.suffix
	}

	return name, nil
}

func renderVolumeName(config *nameTemplateConfig, ctx VolumeNameContext) (string, error) {
	candidate, err := renderVolumeNameCandidate(config, ctx)
	if err != nil {
		return "", err
	}
	legacy := candidate
	if config != nil {
		legacy = sanitizeVolumeName(candidate)
	}
	if config == nil && len(candidate) <= maxBackendNameBytes && validateVolumeName(candidate) == nil {
		return candidate, nil
	}

	stem := sanitizeVolumeNameUnlimited(candidate)
	if stem == "" {
		return "", ErrVolumeNameEmpty
	}
	suffix := "-" + backendNameSuffix(ctx.PVName)
	stem = truncateASCIIName(stem, maxBackendNameBytes-len(suffix))
	name := strings.TrimRight(stem, "-") + suffix
	if err := validateVolumeName(name); err != nil {
		return "", err
	}

	klog.V(4).Infof("Rendered collision-resistant volume name: %s (legacy=%s, request=%s)", name, legacy, ctx.PVName)
	return name, nil
}

func backendNameSuffix(requestName string) string {
	digest := sha256.Sum256([]byte(backendNameHashDomain + requestName))
	value := binary.BigEndian.Uint64(digest[:8])
	var suffix [backendNameSuffixLength]byte
	for i := len(suffix) - 1; i >= 0; i-- {
		suffix[i] = backendNameSuffixAlphabet[value%uint64(len(backendNameSuffixAlphabet))]
		value /= uint64(len(backendNameSuffixAlphabet))
	}
	return string(suffix[:])
}

// sanitizeVolumeName cleans up a volume name to be valid for bcachefs.
// It replaces invalid characters with hyphens, removes leading hyphens,
// and truncates to 63 characters for K8s label compatibility.
func sanitizeVolumeName(name string) string {
	return strings.TrimRight(truncateASCIIName(sanitizeVolumeNameUnlimited(name), maxBackendNameBytes), "-")
}

func sanitizeVolumeNameUnlimited(name string) string {
	var builder strings.Builder
	lastHyphen := false
	for _, char := range name {
		valid := char < 128 && ((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '.' || char == '_' || char == ':' || char == '-')
		if !valid {
			char = '-'
		}
		if char == '-' {
			if builder.Len() == 0 || lastHyphen {
				continue
			}
			lastHyphen = true
		} else {
			lastHyphen = false
		}
		builder.WriteRune(char)
	}
	return strings.Trim(strings.TrimLeft(builder.String(), "._:"), "-")
}

func truncateASCIIName(name string, limit int) string {
	if len(name) <= limit {
		return name
	}
	return name[:limit]
}

// validateVolumeName checks if a volume name is valid for bcachefs.
func validateVolumeName(name string) error {
	if name == "" {
		return ErrVolumeNameEmpty
	}

	if !validNameRegex.MatchString(name) {
		return fmt.Errorf("%w: '%s'", ErrVolumeNameInvalid, name)
	}

	return nil
}

// ResolveVolumeName is the main entry point for volume name resolution.
// It extracts templating configuration from StorageClass parameters,
// builds the template context, and renders the final volume name.
//
// Parameters:
//   - params: StorageClass parameters from the CreateVolumeRequest
//   - pvName: The PV name from req.GetName()
//
// Returns:
//   - The resolved volume name (may be same as pvName if no templating configured)
//   - An error if template parsing or rendering fails
func ResolveVolumeName(params map[string]string, pvName string) (string, error) {
	// Parse template configuration from StorageClass parameters
	config, err := parseNameTemplateConfig(params)
	if err != nil {
		return "", err
	}

	// Extract context from CSI parameters
	ctx := extractVolumeNameContext(params, pvName)

	// Render the final name
	return renderVolumeName(config, ctx)
}

// ResolveLegacyVolumeName returns the unhashed backend candidate produced before
// collision-resistant names were introduced. It is used only for upgrade lookup.
func ResolveLegacyVolumeName(params map[string]string, pvName string) (string, error) {
	config, err := parseNameTemplateConfig(params)
	if err != nil {
		return "", err
	}
	candidate, err := renderVolumeNameCandidate(config, extractVolumeNameContext(params, pvName))
	if err != nil {
		return "", err
	}
	if config == nil {
		return candidate, nil
	}
	legacy := sanitizeVolumeName(candidate)
	if err := validateVolumeName(legacy); err != nil {
		return "", err
	}
	return legacy, nil
}

// ResolveComment resolves a dataset comment from a commentTemplate StorageClass parameter.
// Returns "" if no commentTemplate is configured.
// Unlike volume names, comments are free-form text and are not sanitized or validated.
func ResolveComment(params map[string]string, pvName string) (string, error) {
	templateStr := params[ParamCommentTemplate]
	if templateStr == "" {
		return "", nil
	}

	tmpl, err := template.New("comment").Parse(templateStr)
	if err != nil {
		return "", fmt.Errorf("invalid commentTemplate '%s': %w", templateStr, err)
	}

	ctx := extractVolumeNameContext(params, pvName)

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("failed to execute comment template: %w", err)
	}

	return buf.String(), nil
}
