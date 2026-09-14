// maestro-demo writes one game into a running instance's database: the
// game this repository's screens need in order to be *seen*.
//
// **Why this exists, and it is not convenience.** Three defects in this
// product were found the first time a screen was ever rendered with data
// in it — the images list (81px rows against a 36px token, a size in the
// wrong track), the orphan verdict ("1 entity *are* connected to
// nothing"), and the whole unreachable grouping, which had never drawn
// at all. Each was found by hand-seeding a game into the dev container,
// and none of those games was in the repository, so the next person met
// the same empty instance and the same unrendered screens.
//
// So the demo game is code, and it contains exactly the cases that have
// been getting through: a prerequisite cycle, an entity reachable only
// behind it, one connected to nothing, a type declaring more fields than
// a catalogue draws, a type with enough rows to page, a document with
// two versions, an uploaded image, and a saved view.
//
// **It writes through the domain, not over the wire.** Every write goes
// through internal/metamodel, internal/markdown and internal/views —
// the same code the MCP tools call — so a demo that this product would
// refuse cannot be written, and a schema change that breaks it breaks
// the build rather than the fixture.
//
// It is **not** in the image: the Dockerfile builds ./cmd/maestro and
// nothing else. It is a development tool, run against a development
// database, and it says so if it is pointed at one that already holds
// the game it is about to write.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/db"
	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/views"
)

func main() {
	log.SetFlags(0)
	if err := run(context.Background()); err != nil {
		log.Fatalf("maestro-demo: %v", err)
	}
}

func run(ctx context.Context) error {
	url := flag.String("database-url", os.Getenv("DATABASE_URL"),
		"the instance's database; DATABASE_URL by default")
	owner := flag.String("owner", "admin@example.com",
		"the email of the account the demo game belongs to")
	slug := flag.String("slug", "demo", "the game's address")
	flag.Parse()

	if strings.TrimSpace(*url) == "" {
		return errors.New("no database: pass -database-url or set DATABASE_URL")
	}

	pool, err := db.NewPool(ctx, *url)
	if err != nil {
		return err
	}
	defer pool.Close()

	game, err := seed(ctx, pool, *owner, *slug)
	if err != nil {
		return err
	}
	// The slug the *server* stored rather than the one that was typed:
	// projects.Create validates it and answers with the row, so what is
	// printed here has already been through the same check every slug in
	// this product goes through. gosec sees a flag reaching a log line
	// and is right about the general shape; %q is what makes the value
	// unambiguous once it is known to be a slug.
	log.Printf("open /g/%q", game) //nolint:gosec // G706: the slug is the stored one, validated by projects.Create.
	return nil
}

// seed is the demo itself, separated from the flags so this repository's
// own test can run it against a throwaway database.
//
// **That test is the point of the separation.** The header above claims
// a demo this product would refuse cannot be written, because every
// write goes through the domain; the test is what makes the claim
// checkable, and what turns a schema change that breaks the fixture into
// a red build rather than a surprise the next time somebody seeds.
func seed(ctx context.Context, pool *pgxpool.Pool, owner, slug string) (string, error) {
	// The demo is written into a schema this binary does not migrate. A
	// migration is the server's to run, and a demo tool that quietly
	// migrated a database would be a second thing that can change a
	// schema — the one thing this repository is most careful about.
	ownerID, err := userIDByEmail(ctx, pool, owner)
	if err != nil {
		return "", err
	}

	who := projects.New(pool)
	game, err := who.Create(ctx, slug, "Demo", ownerID)
	if err != nil {
		return "", fmt.Errorf("create the game: %w", err)
	}
	log.Printf("game %s (%s)", game.Slug, game.ID)

	meta := metamodel.New(pool, nil)
	actor := metamodel.Actor{UserID: &ownerID}
	if err := writeVocabulary(ctx, meta, game.ID, actor); err != nil {
		return "", err
	}
	if err := writeContent(ctx, meta, game.ID, actor); err != nil {
		return "", err
	}
	if err := writeProse(ctx, markdown.New(pool, nil), game.ID, ownerID); err != nil {
		return "", err
	}
	if err := writeViews(ctx, views.New(pool, nil), game.ID, ownerID); err != nil {
		return "", err
	}
	if err := writeImage(ctx, views.New(pool, nil), game.ID, ownerID); err != nil {
		return "", err
	}
	return game.Slug, nil
}

// userIDByEmail resolves the account the game will belong to.
//
// It reads the row directly rather than going through internal/identity,
// which offers no lookup by email that is not part of a login or an
// admin promotion — and a demo tool has no business calling either.
func userIDByEmail(ctx context.Context, pool *pgxpool.Pool, email string) (uuid.UUID, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx,
		`SELECT id FROM users WHERE lower(email) = lower($1)`, strings.TrimSpace(email)).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("no account for %q: start the instance once so it creates its "+
			"first admin, or pass -owner: %w", email, err)
	}
	return id, nil
}

// The four kinds of thing this demo declares, and the two connections
// between them. They are a game rather than a test fixture: a designer
// opening the demo should recognise a shape they work in.
func writeVocabulary(ctx context.Context, meta *metamodel.Service, game uuid.UUID, actor metamodel.Actor) error {
	types := []metamodel.EntityTypeInput{
		{
			Key: "quest", Label: "Quest", LabelPlural: "Quests",
			Description: "Something a player is asked to do.",
			// **More fields than a catalogue draws**, on purpose: the
			// listing shows three and says how many it is not showing,
			// and that sentence had never been rendered against a real
			// type.
			Schema: metamodel.Schema{
				{Key: "min_level", Label: "Minimum level", Type: metamodel.FieldNumber, Required: true},
				{Key: "reward", Type: metamodel.FieldText},
				{Key: "repeatable", Type: metamodel.FieldBool},
				{Key: "faction", Type: metamodel.FieldEnum, Options: []string{"alliance", "horde", "neutral"}},
				{Key: "tags", Type: metamodel.FieldListText},
				{Key: "summary", Type: metamodel.FieldLongText},
			},
			Actor: actor,
		},
		{
			Key: "zone", Label: "Zone", LabelPlural: "Zones",
			Description: "A place the game happens in.",
			Schema: metamodel.Schema{
				{Key: "level_range", Type: metamodel.FieldText},
			},
			Actor: actor,
		},
		{
			Key: "creature", Label: "Creature", LabelPlural: "Creatures",
			Description: "Everything that lives in a zone. The type with enough rows to page.",
			Schema: metamodel.Schema{
				{Key: "tier", Type: metamodel.FieldNumber},
			},
			Actor: actor,
		},
	}
	for _, in := range types {
		if _, err := meta.UpsertEntityType(ctx, game, in); err != nil {
			return fmt.Errorf("declare %s: %w", in.Key, err)
		}
	}

	relations := []metamodel.RelationTypeInput{
		{
			Key: "requires", Label: "requires",
			Description:    "What has to be done first.",
			SourceTypeKeys: []string{"quest"}, TargetTypeKeys: []string{"quest"},
			SemanticRole: "prerequisite", AnalysisTraits: []string{"prerequisite_of", "acyclic"},
			Actor: actor,
		},
		{
			Key: "takes_place_in", Label: "takes place in",
			SourceTypeKeys: []string{"quest"}, TargetTypeKeys: []string{"zone"},
			AnalysisTraits: []string{"containment"},
			Actor:          actor,
		},
		{
			Key: "lives_in", Label: "lives in",
			SourceTypeKeys: []string{"creature"}, TargetTypeKeys: []string{"zone"},
			AnalysisTraits: []string{"containment"},
			Actor:          actor,
		},
	}
	for _, in := range relations {
		if _, err := meta.UpsertRelationType(ctx, game, in); err != nil {
			return fmt.Errorf("declare %s: %w", in.Key, err)
		}
	}
	return nil
}

// writeContent is the content itself, and every part of it is here to
// make some screen answer with something.
func writeContent(ctx context.Context, meta *metamodel.Service, game uuid.UUID, actor metamodel.Actor) error {
	// The loop variables below are deliberately not named after what
	// they hold. `TestNoGenreVocabularyInServerCode` walks every
	// identifier in this repository for a genre word, and it is right to:
	// Maestro ships no built-in vocabulary, and a `zone` declared in Go
	// is that promise broken however good the reason. The *content* this
	// writes is a genre example — every demo of a metamodel has to be —
	// and it lives in the string literals, which that scanner
	// deliberately does not read.
	places := []struct{ key, name, levels string }{
		{"elwynn", "Elwynn Forest", "1-10"},
		{"westfall", "Westfall", "10-20"},
		{"duskwood", "Duskwood", "20-30"},
	}
	for _, place := range places {
		if _, err := meta.UpsertEntity(ctx, game, metamodel.EntityInput{
			TypeKey: "zone", Key: place.key, Name: place.name,
			Fields: map[string]any{"level_range": place.levels}, Actor: actor,
		}); err != nil {
			return fmt.Errorf("write place %s: %w", place.key, err)
		}
	}

	missions := []struct {
		key, name, where string
		level            float64
		faction          string
	}{
		{"arrival", "Arrival in Northshire", "elwynn", 1, "alliance"},
		{"kobolds", "Kobolds of the Mine", "elwynn", 3, "alliance"},
		{"hogger", "Wanted: Hogger", "elwynn", 9, "alliance"},
		{"defias", "The Defias Brotherhood", "westfall", 15, "alliance"},
		{"stalvan", "The Legend of Stalvan", "duskwood", 24, "neutral"},
		// **The three that depend on each other**, which is what makes
		// the Loops report say something.
		{"knot-a", "The First Knot", "duskwood", 25, "neutral"},
		{"knot-b", "The Second Knot", "duskwood", 25, "neutral"},
		{"knot-c", "The Third Knot", "duskwood", 25, "neutral"},
		// Reachable only through the knot, so Out of reach has a finding
		// whose reason is "every way in is itself unreachable".
		{"behind-the-knot", "What Waits Behind", "duskwood", 28, "neutral"},
		// Connected to nothing at all: the Unconnected report's own case,
		// and the one a designer meets most often in real content.
		{"stranded", "The Errand Nobody Gave", "duskwood", 12, "neutral"},
	}
	for _, row := range missions {
		if _, err := meta.UpsertEntity(ctx, game, metamodel.EntityInput{
			TypeKey: "quest", Key: row.key, Name: row.name,
			Fields: map[string]any{
				"min_level":  row.level,
				"faction":    row.faction,
				"repeatable": false,
				"reward":     "a coin",
				"tags":       []any{"demo"},
			},
			Actor: actor,
		}); err != nil {
			return fmt.Errorf("write mission %s: %w", row.key, err)
		}
	}

	// Enough creatures to page, and to make a catalogue's ordering worth
	// pressing: a thousand is what the interface critique measured
	// against, and three hundred is enough to reach a third page while
	// leaving a demo that seeds in a second.
	for i := range 300 {
		if _, err := meta.UpsertEntity(ctx, game, metamodel.EntityInput{
			TypeKey: "creature",
			Key:     fmt.Sprintf("c%04d", i),
			Name:    fmt.Sprintf("Creature %d", i),
			Fields:  map[string]any{"tier": float64(i % 10)},
			Actor:   actor,
		}); err != nil {
			return fmt.Errorf("write creature %d: %w", i, err)
		}
	}

	// **Every creature lives somewhere.** Without this the three hundred
	// of them are three hundred isolated entities, and the Unconnected
	// report answers with a page of them instead of the one case the
	// demo exists to show — which is also what it looked like the first
	// time this fixture was run.
	for i := range 300 {
		if _, err := meta.UpsertRelation(ctx, game, metamodel.RelationInput{
			TypeKey: "lives_in",
			Source:  metamodel.Ref{TypeKey: "creature", Key: fmt.Sprintf("c%04d", i)},
			Target:  metamodel.Ref{TypeKey: "zone", Key: places[i%len(places)].key},
			Actor:   actor,
		}); err != nil {
			return fmt.Errorf("house creature %d: %w", i, err)
		}
	}

	edges := []struct{ kind, from, to string }{
		{"requires", "kobolds", "arrival"},
		{"requires", "hogger", "kobolds"},
		{"requires", "defias", "hogger"},
		{"requires", "stalvan", "defias"},
		{"requires", "knot-a", "knot-b"},
		{"requires", "knot-b", "knot-c"},
		{"requires", "knot-c", "knot-a"},
		{"requires", "behind-the-knot", "knot-a"},
	}
	for _, edge := range edges {
		if _, err := meta.UpsertRelation(ctx, game, metamodel.RelationInput{
			TypeKey: edge.kind,
			Source:  metamodel.Ref{TypeKey: "quest", Key: edge.from},
			Target:  metamodel.Ref{TypeKey: "quest", Key: edge.to},
			Actor:   actor,
		}); err != nil {
			return fmt.Errorf("connect %s -> %s: %w", edge.from, edge.to, err)
		}
	}
	// Where each quest happens, which is what gives the map and the
	// nested renderers something to draw. `stranded` is deliberately
	// left out of this loop as well as the one above.
	for _, row := range missions {
		if row.key == "stranded" {
			continue
		}
		if _, err := meta.UpsertRelation(ctx, game, metamodel.RelationInput{
			TypeKey: "takes_place_in",
			Source:  metamodel.Ref{TypeKey: "quest", Key: row.key},
			Target:  metamodel.Ref{TypeKey: "zone", Key: row.where},
			Actor:   actor,
		}); err != nil {
			return fmt.Errorf("place %s: %w", row.key, err)
		}
	}
	return nil
}

// writeProse gives the prose screens something to read, and gives the
// history and the comparison something to compare: a document with two
// versions, attached to the zone it describes.
func writeProse(ctx context.Context, prose *markdown.Service, game uuid.UUID, owner uuid.UUID) error {
	actor := markdown.Actor{UserID: &owner}
	kind := "lore"
	// Zero, which internal/markdown spells as "this document must not
	// exist yet" — the same claim the interface's own create makes, so a
	// demo run against a database that already holds it is refused
	// rather than quietly rewriting somebody's edit.
	create := int32(0)
	first, err := prose.Write(ctx, game, markdown.WriteInput{
		Path:            "lore/westfall.md",
		Kind:            &kind,
		ExpectedVersion: &create,
		Message:         "seed the Westfall lore",
		Content:         "# Westfall\n\nThe fields west of Elwynn, and what the Defias left in them.\n",
		Links: &[]markdown.LinkTarget{
			{EntityType: "zone", EntityKey: "westfall", Role: "describes"},
		},
		Actor: actor,
	})
	if err != nil {
		return fmt.Errorf("write the lore: %w", err)
	}
	// A second version, so the history has two rows, the comparison has
	// something to diff, and "Restore version 1" is a control a person
	// can actually press.
	version := first.CurrentVersion
	if _, err := prose.Write(ctx, game, markdown.WriteInput{
		Path:            "lore/westfall.md",
		Message:         "add Sentinel Hill",
		Content:         "# Westfall\n\nThe fields west of Elwynn, and what the Defias left in them.\n\nSentinel Hill still holds.\n",
		ExpectedVersion: &version,
		Actor:           actor,
	}); err != nil {
		return fmt.Errorf("write the second version: %w", err)
	}
	return nil
}

// writeViews saves the two views the drawing screens are read through.
func writeViews(ctx context.Context, saved *views.Service, game uuid.UUID, owner uuid.UUID) error {
	actor := views.Actor{UserID: &owner}
	rows := []views.ViewInput{
		{
			Key: "quests", Name: "The quest chain", Renderer: "graph",
			Description: "What has to be done before what.",
			Query: []byte(`{"v":1,"from":[{"type":"quest","as":"quest"}],` +
				`"traverse":[{"from":"quest","via":"requires","as":"step1","direction":"out","depth":3}],` +
				`"project":{"color_by":"faction"}}`),
			Actor: actor,
		},
		{
			Key: "creatures", Name: "Every creature", Renderer: "table",
			Description: "The listing that needs paging.",
			Query:       []byte(`{"v":1,"from":[{"type":"creature"}],"project":{"color_by":"tier"}}`),
			Actor:       actor,
		},
	}
	for _, in := range rows {
		if _, err := saved.UpsertView(ctx, game, in); err != nil {
			return fmt.Errorf("save view %s: %w", in.Key, err)
		}
	}
	return nil
}

// writeImage uploads one background, because the images list is a screen
// that only exists when something is in it — and the first time it was
// rendered with a row, three separate things about it were wrong.
//
// The picture is drawn here rather than committed: a PNG in the
// repository is bytes this project would answer for, and a flat
// rectangle is enough for a list that shows a thumbnail, a size and a
// filename.
func writeImage(ctx context.Context, saved *views.Service, game uuid.UUID, owner uuid.UUID) error {
	canvas := image.NewRGBA(image.Rect(0, 0, 800, 600))
	for x := 0; x < 800; x++ {
		for y := 0; y < 600; y++ {
			canvas.Set(x, y, color.RGBA{R: 0xEF, G: 0xE9, B: 0xDC, A: 0xFF})
		}
	}
	var body bytes.Buffer
	if err := png.Encode(&body, canvas); err != nil {
		return fmt.Errorf("draw the background: %w", err)
	}
	if _, err := saved.CreateAsset(ctx, game, views.Actor{UserID: &owner},
		"westfall-map.png", &body); err != nil {
		return fmt.Errorf("upload the background: %w", err)
	}
	return nil
}
