package v1alpha1

import (
	"fmt"
	"path"
	"strings"
)

// PostPath returns the full path for the first POST operation (server base + path).
// Used for registration endpoint suffix. E.g. "/api/v1alpha1/stacks".
func PostPath() (string, error) {
	spec, err := GetSwagger()
	if err != nil {
		return "", fmt.Errorf("loading OpenAPI spec: %w", err)
	}
	base := ""
	if len(spec.Servers) > 0 && spec.Servers[0].URL != "" {
		base = strings.TrimSuffix(spec.Servers[0].URL, "/")
	}
	for _, p := range spec.Paths.InMatchingOrder() {
		if spec.Paths.Value(p).Post != nil {
			return path.Join("/", base, strings.TrimPrefix(p, "/")), nil
		}
	}
	return "", fmt.Errorf("no POST path found in OpenAPI spec")
}
