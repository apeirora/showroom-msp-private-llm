// Package slugpolicy selects names from operator configuration and immutable
// service-cluster identity. Tenant annotations, labels and status are not inputs.
package slugpolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	Legacy     = "Legacy"
	Derived    = "Derived"
	legacyMode = "legacy"
	deriveMode = "derive"
)

var (
	ErrLegacyUnmapped = errors.New("legacy instance has no reviewed slug mapping")
	ErrIdentity       = errors.New("instance identity does not match the slug policy")
)

type configuration struct {
	Cutover string  `json:"cutover"`
	Entries []entry `json:"entries"`
}

type entry struct {
	UID       string `json:"uid"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Mode      string `json:"mode"`
	Slug      string `json:"slug,omitempty"`
}

// Policy is immutable after parsing. Changes require a coordinated restart.
type Policy struct {
	cutover time.Time
	entries map[string]entry
}

type Selection struct {
	Slug   string
	Source string
}

// Parse rejects ambiguous or incomplete migration configuration at startup.
func Parse(reader io.Reader) (*Policy, error) {
	data, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil {
		return nil, fmt.Errorf("read slug policy: %w", err)
	}
	if len(data) > 1<<20 {
		return nil, errors.New("slug policy exceeds 1 MiB")
	}
	if err := rejectDuplicateKeys(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return nil, fmt.Errorf("invalid slug policy JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var config configuration
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode slug policy: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("slug policy must contain exactly one JSON document")
	}
	cutover, err := time.Parse(time.RFC3339, config.Cutover)
	if err != nil || cutover.IsZero() {
		return nil, errors.New("slug policy requires a fixed RFC3339 cutover timestamp")
	}
	policy := &Policy{cutover: cutover, entries: make(map[string]entry, len(config.Entries))}
	slugs := make(map[string]bool, len(config.Entries))
	identities := make(map[string]bool, len(config.Entries))
	for _, item := range config.Entries {
		if err := validateEntry(item); err != nil {
			return nil, err
		}
		identity := item.Namespace + "/" + item.Name
		if _, exists := policy.entries[item.UID]; exists || identities[identity] {
			return nil, errors.New("slug policy contains duplicate instance identity")
		}
		if item.Mode == legacyMode {
			for slug := range slugs {
				if strings.HasPrefix(slug, item.Slug) || strings.HasPrefix(item.Slug, slug) {
					return nil, errors.New("slug policy contains duplicate or prefix-overlapping legacy slugs")
				}
			}
		}
		policy.entries[item.UID] = item
		identities[identity] = true
		if item.Mode == legacyMode {
			slugs[item.Slug] = true
		}
	}
	return policy, nil
}

// encoding/json accepts repeated keys by keeping the last value. Operator
// migration configuration must not have two interpretations of an identity.
func rejectDuplicateKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, container := token.(json.Delim)
	if !container {
		return nil
	}
	switch delimiter {
	case '{':
		keys := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, valid := key.(string)
			if !valid || keys[name] {
				return errors.New("duplicate or invalid object field")
			}
			switch name {
			case "cutover", "entries", "uid", "namespace", "name", "mode", "slug":
			default:
				return errors.New("unknown or noncanonical object field")
			}
			keys[name] = true
			if err := rejectDuplicateKeys(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := rejectDuplicateKeys(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected closing delimiter")
	}
	_, err = decoder.Token()
	return err
}

func validateEntry(item entry) error {
	if item.UID == "" || strings.TrimSpace(item.UID) != item.UID ||
		len(validation.IsDNS1123Label(item.Namespace)) != 0 || len(validation.IsDNS1123Subdomain(item.Name)) != 0 {
		return errors.New("slug policy entry requires UID, namespace and name")
	}
	switch item.Mode {
	case legacyMode:
		if item.Slug == "" || len(validation.IsValidLabelValue(item.Slug)) != 0 || strings.HasPrefix(item.Slug, "llm-") || strings.HasPrefix("llm-", item.Slug) {
			return errors.New("legacy slug must be a nonempty label value outside the reserved llm- prefix")
		}
	case deriveMode:
		if item.Slug != "" {
			return errors.New("derive entries must not supply a slug")
		}
	default:
		return errors.New("slug policy entry mode must be legacy or derive")
	}
	return nil
}

// Select binds a mapping to UID, namespace and name. Unmapped objects created at
// or before the cutoff are never silently renamed. A post-cutoff restore with a
// new UID is indistinguishable from a new object: recovery requires a reviewed map.
func (p *Policy) Select(object metav1.Object) (Selection, error) {
	created := object.GetCreationTimestamp()
	if p == nil || object.GetUID() == "" || created.IsZero() {
		return Selection{}, ErrIdentity
	}
	uid := string(object.GetUID())
	if item, exists := p.entries[uid]; exists {
		if item.Namespace != object.GetNamespace() || item.Name != object.GetName() {
			return Selection{}, ErrIdentity
		}
		if item.Mode == legacyMode {
			return Selection{Slug: item.Slug, Source: Legacy}, nil
		}
	} else if !created.After(p.cutover) {
		return Selection{}, ErrLegacyUnmapped
	}
	digest := sha256.Sum256([]byte("llm.privatellms.msp/slug/v1:" + uid))
	return Selection{Slug: "llm-" + hex.EncodeToString(digest[:16]), Source: Derived}, nil
}
