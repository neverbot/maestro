package web_test

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/analysis"
	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/views"
)

// The wire-tag guards: **a domain type that crosses the wire spells its
// members the way the tools document them.**
//
// This package's standing habit is to project a domain row into a
// tagged `…Output` type before it goes out, so the vocabulary an agent
// reads is decided in one file. A handful of places skip the projection
// and hand a domain type straight to the encoder — mcp_views.go's
// runOutput does it for the whole of views.Result, and several bulk
// outputs embed a domain slice as a tagged field. For those, Go's
// default field naming *is* the wire spelling, and Go's default is the
// Go field name.
//
// That is not a hypothetical. `internal/views.Position` shipped with no
// tags: a run answered `EntityType`/`EntityKey`/`X`/`Y`/`Pinned` while
// views.set_positions took, and every tool description documented,
// `entity_type`/`entity_key`/`x`/`y`/`pinned`. A client reading the
// documented spelling found no stored position in any envelope — not an
// error, not an empty list with a reason, but a view that looks exactly
// like one nobody has ever arranged. The interface's layout composer
// read both spellings as a workaround rather than being able to trust
// one.
//
// A type with no tags is invisible from any one call site, which is how
// that one survived a whole sub-project and its review rounds. So it is
// swept rather than spot-checked: TestEveryDomainTypeOnTheWireIsTagged
// walks the type graph of every root below, and
// TestEveryDomainTypeOnTheWireIsAKnownRoot keeps the root list from
// falling behind the code.

// wireDomainRoots is every type from outside package web that reaches
// the encoder, keyed by the qualified name the source spells it with.
//
// `views.Result` is here by hand and is the one that has to be: it is
// not the type of any tagged field, because runOutput takes it apart
// into a map and puts its members in one at a time. Everything else is
// a tagged field's type, and the companion test below fails if the code
// grows one this map does not name.
var wireDomainRoots = map[string]reflect.Type{
	// The run envelope. runOutput spreads this into a map, so every
	// member of it is a wire value under its own Go-decided name.
	"views.Result": reflect.TypeFor[views.Result](),

	// The analysis domain answers with its own result types rather than
	// with a projection, deliberately: every member of each is already a
	// wire spelling designed as the answer to a tool call, and a
	// projection would be a second place that decides what a finding is.
	// That choice is only safe while this sweep names the roots, so it
	// does — including the two that reach the encoder through no tagged
	// field of this package at all, RouteCheck and RoutePage, which are
	// a tool's whole `Out` value.
	"analysis.CyclesResult":      reflect.TypeFor[analysis.CyclesResult](),
	"analysis.UnreachableResult": reflect.TypeFor[analysis.UnreachableResult](),
	"analysis.OrphansResult":     reflect.TypeFor[analysis.OrphansResult](),
	"analysis.Route":             reflect.TypeFor[analysis.Route](),
	"analysis.RoutePage":         reflect.TypeFor[analysis.RoutePage](),
	"analysis.RouteCheck":        reflect.TypeFor[analysis.RouteCheck](),

	"metamodel.Schema":        reflect.TypeFor[metamodel.Schema](),
	"metamodel.BulkWrite":     reflect.TypeFor[metamodel.BulkWrite](),
	"metamodel.BulkFailure":   reflect.TypeFor[metamodel.BulkFailure](),
	"metamodel.RelationWrite": reflect.TypeFor[metamodel.RelationWrite](),
	"markdown.DocumentWrite":  reflect.TypeFor[markdown.DocumentWrite](),
}

// domainPackages is the set of package names a wire struct can borrow a
// type from. `db` and `dbq` are deliberately absent: a database row is
// not a wire value, and a tagged field holding one would be a finding of
// its own rather than something to check the tags of.
var domainPackages = map[string]bool{
	"analysis": true,
	"views":    true, "metamodel": true, "markdown": true, "identity": true,
	"projects": true, "realtime": true, "roles": true, "paging": true,
	"graph": true, "config": true,
}

// TestEveryDomainTypeOnTheWireIsTagged walks each root's type graph and
// requires every exported field to say what it is called on the wire.
//
// The walk is what makes this a sweep rather than a spot check: a type
// reached only as the element of a slice of a member of a root is
// checked exactly as the root is, because that is how far a client
// reads.
func TestEveryDomainTypeOnTheWireIsTagged(t *testing.T) {
	if len(wireDomainRoots) == 0 {
		t.Fatal("no roots: this guard swept nothing")
	}
	for name, root := range wireDomainRoots {
		seen := map[reflect.Type]bool{}
		walkWireType(t, name, root, seen)
	}
}

// opaqueOnTheWire is a type whose JSON shape is not its fields: it
// either writes itself or is a scalar in disguise. Walking into one
// would judge members no client ever sees.
func opaqueOnTheWire(typ reflect.Type) bool {
	switch typ {
	case reflect.TypeFor[time.Time](), reflect.TypeFor[uuid.UUID](),
		reflect.TypeFor[json.RawMessage]():
		return true
	}
	marshaler := reflect.TypeFor[json.Marshaler]()
	return typ.Implements(marshaler) || reflect.PointerTo(typ).Implements(marshaler)
}

func walkWireType(t *testing.T, path string, typ reflect.Type, seen map[reflect.Type]bool) {
	t.Helper()
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if seen[typ] || opaqueOnTheWire(typ) {
		return
	}
	seen[typ] = true
	switch typ.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		walkWireType(t, path+"[]", typ.Elem(), seen)
	case reflect.Struct:
		for i := range typ.NumField() {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			tag, ok := field.Tag.Lookup("json")
			// **An embedded struct with no tag has no wire name of its
			// own**: encoding/json inlines its fields into the outer
			// object, so what a client reads are that type's members,
			// which the walk below checks exactly as it checks the
			// outer ones. Requiring a tag here would demand a spelling
			// no client ever sees. An anonymous field of a *non*-struct
			// type is not inlined — it crosses the wire under its type's
			// name — so it falls through to the check.
			if !ok && field.Anonymous && embeddedStruct(field.Type) {
				walkWireType(t, path+"."+field.Name, field.Type, seen)
				continue
			}
			if !ok {
				t.Errorf("%s.%s carries no json tag: it crosses the wire as %q, which is a "+
					"Go field name and not the spelling the tools document — a client reading "+
					"the documented one finds nothing, and finding nothing looks exactly like "+
					"there being nothing",
					path, field.Name, field.Name)
				continue
			}
			if strings.HasPrefix(tag, "-") && (tag == "-" || tag == "-,") {
				continue
			}
			walkWireType(t, path+"."+field.Name, field.Type, seen)
		}
	}
}

// TestEveryDomainTypeOnTheWireIsAKnownRoot reads this package's own
// source for a tagged field whose type comes from a domain package, and
// fails if wireDomainRoots does not name it.
//
// Without it the sweep above is a list somebody has to remember to
// extend — which is the failure it exists to close, one level up: a rule
// established correctly and not carried one step along.
func TestEveryDomainTypeOnTheWireIsAKnownRoot(t *testing.T) {
	found := map[string]string{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	fset := token.NewFileSet()
	scanned := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		scanned++
		ast.Inspect(file, func(node ast.Node) bool {
			structType, ok := node.(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range structType.Fields.List {
				if !taggedForJSON(field) {
					continue
				}
				for _, qualified := range domainTypesIn(field.Type) {
					found[qualified] = fmt.Sprintf("%s:%d", name, fset.Position(field.Pos()).Line)
				}
			}
			return true
		})
	}
	if scanned == 0 {
		t.Fatal("scanned no source files: this guard read nothing")
	}

	missing := make([]string, 0)
	for qualified, where := range found {
		if _, known := wireDomainRoots[qualified]; !known {
			missing = append(missing, fmt.Sprintf("%s (%s)", qualified, where))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("these domain types are marshalled as themselves and wireDomainRoots does not "+
			"name them, so nothing checks that their members are spelled the way an agent is "+
			"told to read them: %s", strings.Join(missing, ", "))
	}
}

// embeddedStruct reports whether an anonymous field's type is one
// encoding/json inlines: a struct, or a pointer to one.
func embeddedStruct(typ reflect.Type) bool {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	return typ.Kind() == reflect.Struct
}

func taggedForJSON(field *ast.Field) bool {
	if field.Tag == nil {
		return false
	}
	tag, err := strconv.Unquote(field.Tag.Value)
	if err != nil {
		return false
	}
	value, ok := reflect.StructTag(tag).Lookup("json")
	return ok && value != "-"
}

// domainTypesIn finds every `pkg.Type` a field's type expression names,
// through slices, maps, pointers and arrays, because a client reads
// through all four.
func domainTypesIn(expr ast.Expr) []string {
	var out []string
	ast.Inspect(expr, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok || !domainPackages[pkg.Name] {
			return true
		}
		out = append(out, pkg.Name+"."+selector.Sel.Name)
		return true
	})
	return out
}
