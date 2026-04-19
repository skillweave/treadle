// Package migrations holds the schema-migration registry for
// .loom/project.md. v0.1.0 ships with an empty registry — the first
// real migration lands when a project.md schema bump happens.
package migrations

import (
	"errors"
	"fmt"
)

// SupportedRange is the [min, max] project.md schema_version range
// this build of treadle understands. Updated when a schema bump ships.
var SupportedRange = [2]int{1, 1}

var (
	ErrNoMigrationPath = errors.New("no migration path available for requested schema versions")
	ErrSchemaTooNew    = errors.New("project.md schema_version is newer than this treadle supports")
	ErrSchemaTooOld    = errors.New("project.md schema_version is older than this treadle supports; no downgrade migration exists")
)

// Migration is one step: takes (frontmatter map, body string, from, to)
// and returns the transformed (frontmatter, body). Registered in
// registry below with the exact {from, to} it handles.
type Migration func(fm map[string]any, body string) (map[string]any, string, error)

type registryKey struct {
	From int
	To   int
}

var registry = map[registryKey]Migration{
	// v0.1.0: empty. When the first real migration ships, append here:
	//   {From: 1, To: 2}: migrateV1ToV2,
}

// ApplyMigrations walks the registry to convert a project.md from
// fromVer to toVer. Returns the transformed frontmatter + body, or an
// error if no path exists. Single-hop only in v1; multi-hop composition
// lands with the first migration that needs it.
func ApplyMigrations(fm map[string]any, body string, fromVer, toVer int) (map[string]any, string, error) {
	if fromVer == toVer {
		return fm, body, nil
	}
	if toVer < SupportedRange[0] {
		return nil, "", fmt.Errorf("%w: requested %d, minimum supported is %d", ErrSchemaTooOld, toVer, SupportedRange[0])
	}
	if toVer > SupportedRange[1] {
		return nil, "", fmt.Errorf("%w: requested %d, maximum supported is %d", ErrSchemaTooNew, toVer, SupportedRange[1])
	}
	m, ok := registry[registryKey{From: fromVer, To: toVer}]
	if !ok {
		return nil, "", fmt.Errorf("%w: from=%d to=%d", ErrNoMigrationPath, fromVer, toVer)
	}
	return m(fm, body)
}

// CheckSupported returns nil if schemaVer is within SupportedRange,
// ErrSchemaTooNew / ErrSchemaTooOld otherwise.
func CheckSupported(schemaVer int) error {
	if schemaVer < SupportedRange[0] {
		return fmt.Errorf("%w: got %d, minimum supported is %d", ErrSchemaTooOld, schemaVer, SupportedRange[0])
	}
	if schemaVer > SupportedRange[1] {
		return fmt.Errorf("%w: got %d, maximum supported is %d", ErrSchemaTooNew, schemaVer, SupportedRange[1])
	}
	return nil
}
