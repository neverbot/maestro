package metamodel

import "github.com/neverbot/maestro/internal/roles"

// Event kinds this package publishes into its hub, with the role and
// human-only gating each one carries.
const (
	// eventTypeUpserted and eventTypeRemoved fire from UpsertEntityType
	// and RemoveEntityType once their transaction has committed.
	eventTypeUpserted = "type.upserted"
	eventTypeRemoved  = "type.removed"

	// eventTypeRenamed fires from RenameEntityType once its transaction
	// has committed, under the same gating as its two neighbours: a
	// rename is a change to the game's vocabulary, which is exactly what
	// eventTypeUpserted's argument above is about, and a subscriber that
	// may read every type on demand loses nothing by being told one
	// changed handle.
	eventTypeRenamed = "type.renamed"

	// eventEntityUpserted and eventEntityRemoved fire from UpsertEntity,
	// UpsertEntities and RemoveEntity once their transaction has
	// committed.
	eventEntityUpserted = "entity.upserted"
	eventEntityRemoved  = "entity.removed"

	// eventRelationTypeUpserted and eventRelationTypeRemoved fire from
	// UpsertRelationType and RemoveRelationType once their transaction
	// has committed.
	eventRelationTypeUpserted = "relation_type.upserted"
	eventRelationTypeRemoved  = "relation_type.removed"

	// eventRelationTypeRenamed is eventTypeRenamed for the edge
	// vocabulary, with the same payload and the same argument for being
	// a kind of its own; see it. The gating is this pair's, not that
	// pair's, for the reason all four gating constants below are four
	// and not two.
	eventRelationTypeRenamed = "relation_type.renamed"

	// eventRelationUpserted and eventRelationRemoved fire from
	// UpsertRelation, UpsertRelations and RemoveRelation once their
	// transaction has committed.
	eventRelationUpserted = "relation.upserted"
	eventRelationRemoved  = "relation.removed"
)

// typeEventMinRole and typeEventHumanOnly are the gating decided above,
// named so the two call sites read as the decision rather than as two
// bare literals a later edit could drift apart.
const (
	typeEventMinRole   = roles.Role("")
	typeEventHumanOnly = false
)

// entityEventMinRole and entityEventHumanOnly are the gating decided
// above for entity.upserted and entity.removed. They are their own
// constants rather than an alias of the type-event pair: the two
// decisions happen to agree today, and naming one in terms of the other
// would make a later change to either silently change both.
const (
	entityEventMinRole   = roles.Role("")
	entityEventHumanOnly = false
)

// relationTypeEventMinRole / relationTypeEventHumanOnly and
// relationEventMinRole / relationEventHumanOnly are the gating decided
// above. They are four constants rather than two, and neither pair is an
// alias of the other or of the entity pairs: all four decisions happen to
// agree today, and naming one in terms of another would make a later
// change to either silently change both.
const (
	relationTypeEventMinRole   = roles.Role("")
	relationTypeEventHumanOnly = false

	relationEventMinRole   = roles.Role("")
	relationEventHumanOnly = false
)
