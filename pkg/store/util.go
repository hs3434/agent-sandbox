package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hs3434/agent-sandbox/pkg/resource"
)

const timeRFC3339 = time.RFC3339Nano

func nowOr(v string) string {
	if v != "" {
		return v
	}
	return time.Now().UTC().Format(timeRFC3339)
}

// splitID decomposes "{tenant}:{type}:{name}".
func splitID(id resource.ResourceID) (tenant, typ, name string, err error) {
	parts := strings.SplitN(string(id), ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("malformed resource id %q (want tenant:type:name)", id)
	}
	return parts[0], parts[1], parts[2], nil
}

func encodeTags(tags map[string]string) (string, error) {
	if len(tags) == 0 {
		return "", nil
	}
	var b strings.Builder
	for k, v := range tags {
		fmt.Fprintf(&b, "%s=%s\n", k, v)
	}
	return b.String(), nil
}

func decodeTags(s string) map[string]string {
	if s == "" {
		return nil
	}
	tags := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			tags[k] = v
		}
	}
	return tags
}

type scanner interface{ Scan(dest ...any) error }

func scanResource(row scanner) (*resource.Meta, error) { return scanInto(row) }

func scanInto(sc scanner) (*resource.Meta, error) {
	var m resource.Meta
	var id, typ, status, tags, createdAt, spec string
	if err := sc.Scan(&id, &typ, &m.Backend, &status, &tags, &m.Owner, &createdAt, &spec); err != nil {
		return nil, err
	}
	m.Image = decodeSpecImage(spec)
	m.ID = resource.ResourceID(id)
	m.Type = resource.Type(typ)
	m.Status = resource.Status(status)
	m.Tags = decodeTags(tags)
	if t, err := time.Parse(timeRFC3339, createdAt); err == nil {
		m.CreatedAt = t
	}
	return &m, nil
}

// encodeSpec persists the create-time spec (the DB is the record of what we
// manage — image included).
func encodeSpec(m *resource.Meta) (string, error) {
	if m.Image == "" {
		return "", nil
	}
	b, err := json.Marshal(map[string]string{"image": m.Image})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// decodeSpecImage reads the image back out of the persisted spec.
func decodeSpecImage(spec string) string {
	if spec == "" {
		return ""
	}
	var s struct {
		Image string `json:"image"`
	}
	if err := json.Unmarshal([]byte(spec), &s); err != nil {
		return ""
	}
	return s.Image
}

// isUniqueViolation detects the driver's constraint error without importing
// driver-specific error codes at every call site.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}
