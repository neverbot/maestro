// The name of every renderer parameter, declared once.
//
// The server declares the same set in internal/views/renderers.go, where
// it validates a stored view and describes the catalogue to an agent.
// Two hand-written lists of the same strings drift silently — a renamed
// parameter validates on one side and reads as absent on the other, and
// the picture simply comes out without that knob. checkRendererParams
// (internal/web/renderer_params.go) reads this file at start-up and
// refuses to serve when the two disagree.

// graph
export const PARAM_COLOR_BY = "color_by";
export const PARAM_SIZE_BY = "size_by";
export const PARAM_CLUSTER_BY = "cluster_by";
export const PARAM_EDGE_LABELS = "edge_labels";
export const PARAM_ARROWS = "arrows";

// layered
export const PARAM_RANK_DIRECTION = "rank_direction";
export const PARAM_RANK_BY = "rank_by";
export const PARAM_LAYER_LABELS = "layer_labels";
export const PARAM_ALIGN = "align";

// map
export const PARAM_COORDINATE_SOURCE = "coordinate_source";
export const PARAM_X_FIELD = "x_field";
export const PARAM_Y_FIELD = "y_field";
export const PARAM_SNAP = "snap";

// nested
export const PARAM_CONTAIN_VIA = "contain_via";
export const PARAM_MAX_DEPTH = "max_depth";
export const PARAM_LEAF_LABEL = "leaf_label";

// table
export const PARAM_COLUMNS = "columns";
export const PARAM_SORT = "sort";
export const PARAM_PAGE_SIZE = "page_size";

// timeline
export const PARAM_AXIS_FIELD = "axis_field";
export const PARAM_AXIS_END_FIELD = "axis_end_field";
export const PARAM_LANE_BY = "lane_by";
export const PARAM_AXIS_LABEL = "axis_label";

// Read by both the graph and the table.
export const PARAM_GROUP_BY = "group_by";
