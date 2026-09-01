package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

type Node struct {
	ID                     int64   `json:"id"`
	Type                   string  `json:"type"` // self_built | external
	Name                   string  `json:"name"`
	Protocol               string  `json:"protocol"`
	InboundTag             string  `json:"inbound_tag"`
	RouteUpstreamInboundID int64   `json:"route_upstream_inbound_id"`
	RouteUpstreamBroken    bool    `json:"route_upstream_broken"`
	ShareLink              string  `json:"share_link"`
	SourceID               int64   `json:"source_id"`
	Enabled                bool    `json:"enabled"`
	SortOrder              int64   `json:"sort_order"`
	CreatedAt              int64   `json:"created_at"`
	GroupIDs               []int64 `json:"group_ids,omitempty"`
}

const nodeCols = `id, type, name, protocol, inbound_tag, route_upstream_inbound_id, route_upstream_broken, share_link, source_id, enabled, sort_order, created_at`

func scanNode(sc scanner) (*Node, error) {
	var n Node
	var routeBroken int
	err := sc.Scan(&n.ID, &n.Type, &n.Name, &n.Protocol, &n.InboundTag,
		&n.RouteUpstreamInboundID, &routeBroken, &n.ShareLink, &n.SourceID, &n.Enabled, &n.SortOrder, &n.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	n.RouteUpstreamBroken = routeBroken == 1
	return &n, nil
}

func (s *Store) GetNode(id int64) (*Node, error) {
	return scanNode(s.db.QueryRow(`SELECT `+nodeCols+` FROM nodes WHERE id=?`, id))
}

func (s *Store) ListNodes() ([]*Node, error) {
	rows, err := s.db.Query(`SELECT ` + nodeCols + ` FROM nodes ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// attach group ids
	for _, n := range out {
		gids, err := s.NodeGroupIDs(n.ID)
		if err != nil {
			return nil, err
		}
		n.GroupIDs = gids
	}
	return out, nil
}

// SelfBuiltNodeNames maps each bound inbound tag to the display name the admin
// gave that node on the 节点 page. Used as the subscription remark so clients
// show the configured name instead of the raw inbound tag. A tag bound by more
// than one node keeps the first (sort_order, id) — the same order the node page
// lists them in.
func (s *Store) SelfBuiltNodeNames() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT inbound_tag, name FROM nodes
		WHERE type='self_built' AND inbound_tag != '' AND name != ''
		  AND route_upstream_inbound_id=0 AND route_upstream_broken=0
		ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var tag, name string
		if err := rows.Scan(&tag, &name); err != nil {
			return nil, err
		}
		if _, dup := out[tag]; !dup {
			out[tag] = name
		}
	}
	return out, rows.Err()
}

func (s *Store) CreateNode(n Node) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO nodes
		(type, name, protocol, inbound_tag, route_upstream_inbound_id, route_upstream_broken, share_link, source_id, enabled, sort_order, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		n.Type, n.Name, n.Protocol, n.InboundTag, n.RouteUpstreamInboundID, boolToInt(n.RouteUpstreamBroken), n.ShareLink, n.SourceID,
		boolToInt(n.Enabled), n.SortOrder, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if n.GroupIDs != nil {
		if err := s.SetNodeGroups(id, n.GroupIDs); err != nil {
			return id, err
		}
	}
	return id, nil
}

func (s *Store) UpdateNode(n Node) error {
	_, err := s.db.Exec(`UPDATE nodes SET
		type=?, name=?, protocol=?, inbound_tag=?, route_upstream_inbound_id=?, route_upstream_broken=?, share_link=?, enabled=?, sort_order=? WHERE id=?`,
		n.Type, n.Name, n.Protocol, n.InboundTag, n.RouteUpstreamInboundID, boolToInt(n.RouteUpstreamBroken), n.ShareLink, boolToInt(n.Enabled), n.SortOrder, n.ID)
	if err != nil {
		return err
	}
	if n.GroupIDs != nil {
		return s.SetNodeGroups(n.ID, n.GroupIDs)
	}
	return nil
}

// ReorderNodes sets sort_order to each id's position in the given slice, so the
// node page and generated subscriptions (both ORDER BY sort_order) render in this
// exact order. Nodes group by membership but sort_order is global, so callers
// reorder by swapping global positions; ids not listed keep their old value.
func (s *Store) ReorderNodes(ids []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range ids {
		if _, err := tx.Exec(`UPDATE nodes SET sort_order=? WHERE id=?`, i, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteNode(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM node_group_members WHERE node_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM nodes WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- groups ----

type NodeGroup struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IsAI        bool   `json:"is_ai"`
	SortOrder   int64  `json:"sort_order"`
	CreatedAt   int64  `json:"created_at"`
}

// GroupCount returns how many node groups exist. Used to tell "grouping not set
// up at all" (zero-config) apart from "this user is assigned no group".
func (s *Store) GroupCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM node_groups`).Scan(&n)
	return n, err
}

func (s *Store) ListGroups() ([]*NodeGroup, error) {
	rows, err := s.db.Query(`SELECT id, name, description, is_ai, sort_order, created_at FROM node_groups ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*NodeGroup
	for rows.Next() {
		var g NodeGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.IsAI, &g.SortOrder, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &g)
	}
	return out, rows.Err()
}

func (s *Store) CreateGroup(g NodeGroup) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO node_groups (name, description, is_ai, sort_order, created_at) VALUES (?,?,?,?,?)`,
		g.Name, g.Description, g.IsAI, g.SortOrder, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateGroup(g NodeGroup) error {
	_, err := s.db.Exec(`UPDATE node_groups SET name=?, description=?, is_ai=?, sort_order=? WHERE id=?`,
		g.Name, g.Description, g.IsAI, g.SortOrder, g.ID)
	return err
}

func (s *Store) DeleteGroup(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM node_group_members WHERE group_id=?`,
		`DELETE FROM plan_groups WHERE group_id=?`,
		`DELETE FROM node_groups WHERE id=?`,
		// Clear the free-group pointer if it referenced this group, else
		// AccessibleGroupIDs keeps granting access to a non-existent group.
		`DELETE FROM settings WHERE key='free_group_id' AND value=CAST(? AS TEXT)`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateSettingsCache() // the settings row may have changed
	return nil
}

// ---- membership ----

func (s *Store) SetNodeGroups(nodeID int64, groupIDs []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM node_group_members WHERE node_id=?`, nodeID); err != nil {
		return err
	}
	for _, gid := range groupIDs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO node_group_members (node_id, group_id) VALUES (?,?)`, nodeID, gid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) NodeGroupIDs(nodeID int64) ([]int64, error) {
	return queryInts(s.db, `SELECT group_id FROM node_group_members WHERE node_id=? ORDER BY group_id`, nodeID)
}

// SelfBuiltNodeGroupIDs returns the group ids of the self_built node whose
// sing-box/sing-box inbound tag matches tag (used to map an sb_inbound to the users
// entitled to it).
func (s *Store) SelfBuiltNodeGroupIDs(tag string) ([]int64, error) {
	return queryInts(s.db, `SELECT m.group_id FROM nodes n
		JOIN node_group_members m ON m.node_id = n.id
		WHERE n.type='self_built' AND n.inbound_tag=?
		  AND n.route_upstream_inbound_id=0 AND n.route_upstream_broken=0`, tag)
}

// ---- plan ↔ groups ----

func (s *Store) SetPlanGroups(packageID int64, groupIDs []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM plan_groups WHERE package_id=?`, packageID); err != nil {
		return err
	}
	for _, gid := range groupIDs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO plan_groups (package_id, group_id) VALUES (?,?)`, packageID, gid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) PlanGroupIDs(packageID int64) ([]int64, error) {
	return queryInts(s.db, `SELECT group_id FROM plan_groups WHERE package_id=? ORDER BY group_id`, packageID)
}

// ---- aggregation ----

// AccessibleGroupIDs returns the groups a user can use. It deliberately derives
// access through orderBuckets, the same ownership calculation used for native
// sing-box inbounds: exhausted plans stop granting groups, while a funded pool
// or active general grant can cover groups from a positive plan entitlement.
// A zero-limit plan never establishes that entitlement.
func (s *Store) AccessibleGroupIDs(u *User) ([]int64, error) {
	set := map[int64]bool{}
	free, _ := s.GetSettingInt64("free_group_id", 0)
	if free > 0 {
		// Preserve legacy external-free-node visibility even if an old account has
		// not received its separate metering bucket yet.
		set[free] = true
	}
	buckets, err := s.ListBuckets(u.ID)
	if err != nil {
		return nil, err
	}
	groupsByPackage := map[int64][]int64{}
	for _, b := range buckets {
		if b.Kind != "plan" || b.PackageID <= 0 || b.Status == "queued" || b.TrafficLimit <= 0 {
			continue
		}
		if _, ok := groupsByPackage[b.PackageID]; ok {
			continue
		}
		groupsByPackage[b.PackageID], err = s.PlanGroupIDs(b.PackageID)
		if err != nil {
			return nil, err
		}
	}
	for _, owned := range orderBuckets(buckets, time.Now().Unix(), free, func(packageID int64) []int64 {
		return groupsByPackage[packageID]
	}) {
		for g := range owned.groups {
			set[g] = true
		}
	}
	out := make([]int64, 0, len(set))
	for g := range set {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// GroupedNode is a node plus the (smallest) group id it belongs to among a
// queried accessible-group set — for attributing a node to one group in the UI.
type GroupedNode struct {
	*Node
	GroupID int64 `json:"group_id"`
	IsAI    bool  `json:"is_ai"`
}

// NodesInGroupsTagged returns enabled nodes in any of groupIDs, each tagged with
// the smallest matching group id and whether any matching group is AI-marked
// (one row per node, deduped).
func (s *Store) NodesInGroupsTagged(groupIDs []int64) ([]GroupedNode, error) {
	if len(groupIDs) == 0 {
		return nil, nil
	}
	ph := make([]string, len(groupIDs))
	args := make([]any, len(groupIDs))
	for i, g := range groupIDs {
		ph[i] = "?"
		args[i] = g
	}
	in := strings.Join(ph, ",")
	q := `SELECT ` + nodeColsPrefixed("n") + `, MIN(m.group_id), MAX(g.is_ai) FROM nodes n
		JOIN node_group_members m ON m.node_id = n.id
		JOIN node_groups g ON g.id = m.group_id
		WHERE n.enabled = 1 AND m.group_id IN (` + in + `)
		GROUP BY n.id
		ORDER BY n.sort_order, n.id`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GroupedNode
	for rows.Next() {
		var n Node
		var gid int64
		var isAI bool
		var routeBroken int
		if err := rows.Scan(&n.ID, &n.Type, &n.Name, &n.Protocol, &n.InboundTag, &n.RouteUpstreamInboundID, &routeBroken, &n.ShareLink,
			&n.SourceID, &n.Enabled, &n.SortOrder, &n.CreatedAt, &gid, &isAI); err != nil {
			return nil, err
		}
		n.RouteUpstreamBroken = routeBroken == 1
		nn := n
		out = append(out, GroupedNode{Node: &nn, GroupID: gid, IsAI: isAI})
	}
	return out, rows.Err()
}

// NodesInGroups returns enabled nodes that belong to any of the given groups.
func (s *Store) NodesInGroups(groupIDs []int64) ([]*Node, error) {
	if len(groupIDs) == 0 {
		return nil, nil
	}
	ph := make([]string, len(groupIDs))
	args := make([]any, len(groupIDs))
	for i, g := range groupIDs {
		ph[i] = "?"
		args[i] = g
	}
	q := `SELECT DISTINCT ` + nodeColsPrefixed("n") + ` FROM nodes n
		JOIN node_group_members m ON m.node_id = n.id
		WHERE n.enabled = 1 AND m.group_id IN (` + strings.Join(ph, ",") + `)
		ORDER BY n.sort_order, n.id`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ---- node sources ----

type NodeSource struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	URL         string  `json:"url"`
	Enabled     bool    `json:"enabled"`
	LastFetched int64   `json:"last_fetched"`
	LastCount   int64   `json:"last_count"`
	LastError   string  `json:"last_error"`
	GroupIDs    []int64 `json:"group_ids"`
	CreatedAt   int64   `json:"created_at"`
}

// marshalGroupIDs encodes a group id list as a JSON array for the group_ids
// column; an empty/nil list becomes "" so SourceGroupIDs returns nil.
func marshalGroupIDs(ids []int64) string {
	if len(ids) == 0 {
		return ""
	}
	b, _ := json.Marshal(ids)
	return string(b)
}

func unmarshalGroupIDs(s string) []int64 {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var ids []int64
	_ = json.Unmarshal([]byte(s), &ids)
	return ids
}

func (s *Store) ListSources() ([]*NodeSource, error) {
	rows, err := s.db.Query(`SELECT id, name, url, enabled, last_fetched, last_count, last_error, group_ids, created_at FROM node_sources ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*NodeSource
	for rows.Next() {
		var n NodeSource
		var gids string
		if err := rows.Scan(&n.ID, &n.Name, &n.URL, &n.Enabled, &n.LastFetched, &n.LastCount, &n.LastError, &gids, &n.CreatedAt); err != nil {
			return nil, err
		}
		n.GroupIDs = unmarshalGroupIDs(gids)
		out = append(out, &n)
	}
	return out, rows.Err()
}

func (s *Store) GetSource(id int64) (*NodeSource, error) {
	var n NodeSource
	var gids string
	err := s.db.QueryRow(`SELECT id, name, url, enabled, last_fetched, last_count, last_error, group_ids, created_at FROM node_sources WHERE id=?`, id).
		Scan(&n.ID, &n.Name, &n.URL, &n.Enabled, &n.LastFetched, &n.LastCount, &n.LastError, &gids, &n.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	n.GroupIDs = unmarshalGroupIDs(gids)
	return &n, nil
}

func (s *Store) CreateSource(n NodeSource) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO node_sources (name, url, enabled, group_ids, created_at) VALUES (?,?,?,?,?)`,
		n.Name, n.URL, boolToInt(n.Enabled), marshalGroupIDs(n.GroupIDs), time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateSource(n NodeSource) error {
	_, err := s.db.Exec(`UPDATE node_sources SET name=?, url=?, enabled=?, group_ids=? WHERE id=?`,
		n.Name, n.URL, boolToInt(n.Enabled), marshalGroupIDs(n.GroupIDs), n.ID)
	return err
}

func (s *Store) DeleteSource(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Group memberships are keyed by node id and are not ON DELETE CASCADE, so
	// they must go with the nodes. Source sync runs on a timer and deletes and
	// reinserts every node under new ids, which otherwise grows this table
	// without bound (DeleteNode already does this correctly).
	if _, err := tx.Exec(`DELETE FROM node_group_members WHERE node_id IN (SELECT id FROM nodes WHERE source_id=?)`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM nodes WHERE source_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM node_sources WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ReplaceSourceNodes swaps in freshly-fetched nodes for a source and records the
// fetch result. groupIDs (optional) are applied to all imported nodes.
func (s *Store) ReplaceSourceNodes(sourceID int64, nodes []Node, groupIDs []int64, fetchErr string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if fetchErr == "" {
		// Group memberships are keyed by node id and are not ON DELETE CASCADE, so
		// they must go with the nodes. Source sync runs on a timer and deletes and
		// reinserts every node under new ids, which otherwise grows this table
		// without bound (DeleteNode already does this correctly).
		if _, err := tx.Exec(`DELETE FROM node_group_members WHERE node_id IN (SELECT id FROM nodes WHERE source_id=?)`, sourceID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM nodes WHERE source_id=?`, sourceID); err != nil {
			return err
		}
		for _, n := range nodes {
			res, err := tx.Exec(`INSERT INTO nodes (type, name, protocol, share_link, source_id, enabled, created_at)
				VALUES ('external', ?, ?, ?, ?, 1, ?)`, n.Name, n.Protocol, n.ShareLink, sourceID, now)
			if err != nil {
				return err
			}
			nid, _ := res.LastInsertId()
			for _, gid := range groupIDs {
				if _, err := tx.Exec(`INSERT OR IGNORE INTO node_group_members (node_id, group_id) VALUES (?,?)`, nid, gid); err != nil {
					return err
				}
			}
		}
	}
	if _, err := tx.Exec(`UPDATE node_sources SET last_fetched=?, last_count=?, last_error=? WHERE id=?`,
		now, len(nodes), fetchErr, sourceID); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- helpers ----

func nodeColsPrefixed(p string) string {
	cols := strings.Split(nodeCols, ", ")
	for i := range cols {
		cols[i] = p + "." + cols[i]
	}
	return strings.Join(cols, ", ")
}

func queryInts(db *sql.DB, q string, args ...any) ([]int64, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
