package waf

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"go.mongodb.org/mongo-driver/bson"
)

// modernShape is the JSON wire format served by the HTTP API.
// FE consumes this on GET and may send it on POST/PUT.
type modernShape struct {
	Sets                   []modernSet       `json:"sets"`
	DefaultSet             string            `json:"default_set"`
	MetricLabels           map[string]string `json:"metric_labels,omitempty"`
	PerAuthorityDirectives map[string]string `json:"per_authority_directives,omitempty"`
}

type modernSet struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Directives  []string `json:"directives"`
}

// legacyShape is the persistent format on disk (BSON) AND the JSON shape
// consumed by the Coraza WASM plugin. It is also what older HTTP clients
// (pre-redesign) may send.
//
// Do NOT add fields here without considering the WASM plugin's parser —
// unknown fields are silently ignored, but renaming or removing
// directives_map/default_directives breaks the data plane.
type legacyShape struct {
	DirectivesMap     map[string][]string `json:"directives_map" bson:"directives_map"`
	DefaultDirectives string              `json:"default_directives" bson:"default_directives"`
	// IMPORTANT: do NOT add `omitempty` to the JSON tags on metric_labels or
	// per_authority_directives. The pre-refactor code emitted them
	// unconditionally (as `{}` or `null`) and the Coraza WASM plugin parses
	// the JSON expecting these keys to always be present. Byte-equivalence
	// with the old wire format is the safest contract.
	MetricLabels           map[string]string `json:"metric_labels" bson:"metric_labels"`
	PerAuthorityDirectives map[string]string `json:"per_authority_directives" bson:"per_authority_directives"`
	// SetDescriptions is BSON-only; the json:"-" tag keeps it out of the
	// WASM-facing JSON payload (the WASM plugin has no use for descriptions
	// and we don't want to surprise it with new fields).
	SetDescriptions map[string]string `json:"-" bson:"set_descriptions,omitempty"`
}

// ============================================================================
// Conversion helpers
// ============================================================================

// toModernShape converts canonical WAFConfigData to the HTTP API JSON shape.
// Sets is always emitted as an empty array (never null) when no sets exist.
func (d WAFConfigData) toModernShape() modernShape {
	sets := make([]modernSet, 0, len(d.Sets))
	for _, s := range d.Sets {
		directives := s.Directives
		if directives == nil {
			directives = []string{}
		}
		sets = append(sets, modernSet{
			Name:        s.Name,
			Description: s.Description,
			Directives:  directives,
		})
	}
	return modernShape{
		Sets:                   sets,
		DefaultSet:             d.DefaultSet,
		MetricLabels:           d.MetricLabels,
		PerAuthorityDirectives: d.PerAuthorityDirectives,
	}
}

// toLegacyShape converts canonical WAFConfigData to the disk/WASM shape.
// directives_map is always emitted as a non-nil map (Coraza WASM expects an
// object). Per-set descriptions are folded into a parallel set_descriptions
// map for disk persistence; the WASM plugin never sees them (json:"-").
func (d WAFConfigData) toLegacyShape() legacyShape {
	dm := make(map[string][]string, len(d.Sets))
	var descriptions map[string]string
	for _, s := range d.Sets {
		directives := s.Directives
		if directives == nil {
			directives = []string{}
		}
		dm[s.Name] = directives
		if s.Description != "" {
			if descriptions == nil {
				descriptions = make(map[string]string, len(d.Sets))
			}
			descriptions[s.Name] = s.Description
		}
	}
	return legacyShape{
		DirectivesMap:          dm,
		DefaultDirectives:      d.DefaultSet,
		MetricLabels:           d.MetricLabels,
		PerAuthorityDirectives: d.PerAuthorityDirectives,
		SetDescriptions:        descriptions,
	}
}

// fromModernShape lifts the HTTP wire shape into the canonical struct.
func fromModernShape(m modernShape) WAFConfigData {
	sets := make([]DirectiveSet, 0, len(m.Sets))
	for _, s := range m.Sets {
		sets = append(sets, DirectiveSet{
			Name:        s.Name,
			Description: s.Description,
			Directives:  s.Directives,
		})
	}
	return WAFConfigData{
		Sets:                   sets,
		DefaultSet:             m.DefaultSet,
		MetricLabels:           m.MetricLabels,
		PerAuthorityDirectives: m.PerAuthorityDirectives,
	}
}

// fromLegacyShape lifts the disk/wire legacy shape into the canonical struct.
// Set ordering from a Go map is non-deterministic, so we sort by name to
// guarantee stable round-trips and audit diffs.
func fromLegacyShape(l legacyShape) WAFConfigData {
	names := make([]string, 0, len(l.DirectivesMap))
	for name := range l.DirectivesMap {
		names = append(names, name)
	}
	sort.Strings(names)

	sets := make([]DirectiveSet, 0, len(names))
	for _, name := range names {
		sets = append(sets, DirectiveSet{
			Name:        name,
			Description: l.SetDescriptions[name],
			Directives:  l.DirectivesMap[name],
		})
	}
	return WAFConfigData{
		Sets:                   sets,
		DefaultSet:             l.DefaultDirectives,
		MetricLabels:           l.MetricLabels,
		PerAuthorityDirectives: l.PerAuthorityDirectives,
	}
}

// ============================================================================
// JSON — used by the HTTP API.
//
// UnmarshalJSON accepts either shape so legacy clients continue to work
// during the transition. MarshalJSON always emits the modern shape.
//
// The WASM-injection path explicitly bypasses this MarshalJSON — see
// wasm_injector.go, which calls json.Marshal(data.toLegacyShape()) directly.
// ============================================================================

// UnmarshalJSON accepts either the modern or the legacy shape on the wire.
// Precedence: if "sets" key is present (even as an empty array), treat the
// payload as modern; otherwise fall back to legacy. The validator rejects
// payloads that result in zero sets.
func (d *WAFConfigData) UnmarshalJSON(b []byte) error {
	var probe struct {
		Sets          json.RawMessage `json:"sets"`
		DirectivesMap json.RawMessage `json:"directives_map"`
	}
	_ = json.Unmarshal(b, &probe)

	if probe.Sets != nil {
		var m modernShape
		if err := json.Unmarshal(b, &m); err != nil {
			return fmt.Errorf("waf config: modern shape decode failed: %w", err)
		}
		*d = fromModernShape(m)
		return nil
	}

	if probe.DirectivesMap != nil {
		var l legacyShape
		if err := json.Unmarshal(b, &l); err != nil {
			return fmt.Errorf("waf config: legacy shape decode failed: %w", err)
		}
		*d = fromLegacyShape(l)
		return nil
	}

	return errors.New("waf config: payload must contain either 'sets' or 'directives_map'")
}

// MarshalJSON emits the modern shape. Always.
func (d WAFConfigData) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.toModernShape())
}

// ============================================================================
// BSON — disk format.
//
// We always write the legacy shape (`directives_map`, `default_directives`,
// `set_descriptions`) so old binaries running mid-rolling-deploy can still
// decode docs written by new binaries. Do NOT change MarshalBSON without a
// coordinated fleet restart and migration plan.
// ============================================================================

// UnmarshalBSON reads the legacy shape from MongoDB.
func (d *WAFConfigData) UnmarshalBSON(b []byte) error {
	var l legacyShape
	if err := bson.Unmarshal(b, &l); err != nil {
		return fmt.Errorf("waf config bson decode: %w", err)
	}
	*d = fromLegacyShape(l)
	return nil
}

// MarshalBSON writes the legacy shape to MongoDB. The disk contract must
// remain stable; see file-level comment.
func (d WAFConfigData) MarshalBSON() ([]byte, error) {
	return bson.Marshal(d.toLegacyShape())
}
