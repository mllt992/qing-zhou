package store

import (
	"context"
	"database/sql"
	"sort"
	"time"
)

// Audience is current entitlement, not concurrent connections. Usage belongs to
// the displayed allowance, never an assertion about traffic on a particular node.
type AudienceUser struct {
	UserID   int64     `json:"user_id"`
	Username string    `json:"username"`
	Buckets  []*Bucket `json:"buckets"`
}
type AudienceSnapshot struct {
	NodeCounts  map[int64]int
	GroupCounts map[int64]int
	Nodes       map[int64][]AudienceUser `json:"-"`
	Groups      map[int64][]AudienceUser `json:"-"`
	Servers     map[int64][]AudienceUser `json:"-"`
	Packages    map[int64][]AudienceUser `json:"-"`
}

// CurrentAudience reads one consistent snapshot with a fixed number of queries;
// no query per node or user, and no arbitrary user-list cap. No credentials are
// read or returned. The canonical orderBuckets resolver remains authoritative.
func (s *Store) CurrentAudience() (AudienceSnapshot, error) { return s.currentAudience("", 0, false) }
func (s *Store) AudienceCounts() (AudienceSnapshot, error)  { return s.currentAudience("", 0, true) }
func (s *Store) ScopedAudience(scope string, id int64) (AudienceSnapshot, error) {
	return s.currentAudience(scope, id, false)
}
func (s *Store) currentAudience(scope string, target int64, countsOnly bool) (AudienceSnapshot, error) {
	out := AudienceSnapshot{NodeCounts: map[int64]int{}, GroupCounts: map[int64]int{}, Nodes: map[int64][]AudienceUser{}, Groups: map[int64][]AudienceUser{}, Servers: map[int64][]AudienceUser{}, Packages: map[int64][]AudienceUser{}}
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	users := map[int64]*User{}
	rows, err := tx.Query(`SELECT id,username,role,email_verified,email_gate_exempt FROM users WHERE status!='banned' ORDER BY id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		u := &User{}
		if err = rows.Scan(&u.ID, &u.Username, &u.Role, &u.EmailVerified, &u.EmailGateExempt); err != nil {
			rows.Close()
			return out, err
		}
		users[u.ID] = u
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	bs := map[int64][]*Bucket{}
	rows, err = tx.Query(`SELECT p.id,p.user_id,p.kind,p.package_id,p.name,p.traffic_limit,p.used_up,p.used_down,p.expiry_at,p.status FROM user_plans p JOIN users u ON u.id=p.user_id WHERE u.status!='banned' ORDER BY p.id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		b := &Bucket{}
		if err = rows.Scan(&b.ID, &b.UserID, &b.Kind, &b.PackageID, &b.Name, &b.TrafficLimit, &b.UsedUp, &b.UsedDown, &b.ExpiryAt, &b.Status); err != nil {
			rows.Close()
			return out, err
		}
		bs[b.UserID] = append(bs[b.UserID], b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	pg := map[int64][]int64{}
	rows, err = tx.Query(`SELECT package_id,group_id FROM plan_groups`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var p, g int64
		if err = rows.Scan(&p, &g); err != nil {
			rows.Close()
			return out, err
		}
		pg[p] = append(pg[p], g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	var free int64
	err = tx.QueryRow(`SELECT CAST(value AS INTEGER) FROM settings WHERE key='free_group_id'`).Scan(&free)
	if err != nil && err != sql.ErrNoRows {
		return out, err
	}
	var verifySetting string
	err = tx.QueryRow(`SELECT value FROM settings WHERE key='email_verify_required'`).Scan(&verifySetting)
	if err != nil && err != sql.ErrNoRows {
		return out, err
	}
	verifyRequired := verifySetting == "true" || verifySetting == "1"
	// Resolve fixed-exit chains with the same fail-closed check used when
	// rendering subscriptions; missing, disabled or cyclic hops grant no node.
	inbounds := []*SbInbound{}
	rows, err = tx.Query(`SELECT id,enabled,upstream_inbound_id FROM sb_inbounds`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		i := &SbInbound{}
		if err = rows.Scan(&i.ID, &i.Enabled, &i.UpstreamInboundID); err != nil {
			rows.Close()
			return out, err
		}
		inbounds = append(inbounds, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	healthy := map[int64]bool{}
	type audienceNode struct {
		id, group, server, upstream int64
		external                    bool
	}
	nodes := map[int64][]audienceNode{}
	nodeCondition := ""
	var nodeArgs []any
	switch scope {
	case "node":
		nodeCondition = " AND n.id=?"
		nodeArgs = append(nodeArgs, target)
	case "group":
		nodeCondition = " AND ng.group_id=?"
		nodeArgs = append(nodeArgs, target)
	case "server":
		nodeCondition = " AND i.server_id=?"
		nodeArgs = append(nodeArgs, target)
	case "package":
		nodeCondition = " AND 0=1"
	}
	rows, err = tx.Query(`SELECT n.id,ng.group_id,COALESCE(i.server_id,0),n.type='external',n.route_upstream_inbound_id FROM nodes n JOIN node_group_members ng ON ng.node_id=n.id LEFT JOIN sb_inbounds i ON i.tag=n.inbound_tag WHERE n.enabled=1 AND (n.type='external' OR (i.enabled=1 AND n.route_upstream_broken=0))`+nodeCondition, nodeArgs...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var n audienceNode
		if err = rows.Scan(&n.id, &n.group, &n.server, &n.external, &n.upstream); err != nil {
			rows.Close()
			return out, err
		}
		if !n.external && n.upstream != 0 {
			good, checked := healthy[n.upstream]
			if !checked {
				good = inboundRouteHealthy(inbounds, n.upstream)
				healthy[n.upstream] = good
			}
			if !good {
				continue
			}
		}
		nodes[n.group] = append(nodes[n.group], n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	now := time.Now().Unix()
	ids := make([]int64, 0, len(users))
	for uid := range users {
		ids = append(ids, uid)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, uid := range ids {
		u := users[uid]
		// Same admission gate as API.emailBlocksSub and HasLivePaidPlan:
		// queued paid plans count as admission, welcome/free allowances do not.
		if verifyRequired && u.Role != "admin" && !u.EmailVerified && !u.EmailGateExempt {
			admitted := false
			for _, b := range bs[uid] {
				if b.Kind == "plan" && b.PackageID >= 0 && (b.Status == "active" || b.Status == "queued") && b.NotExpired(now) {
					admitted = true
					break
				}
			}
			if !admitted {
				continue
			}
		}
		owned := orderBuckets(bs[uid], now, free, func(id int64) []int64 { return pg[id] })
		groups := map[int64][]*Bucket{}
		packages := map[int64][]*Bucket{}
		for _, o := range owned {
			for g := range o.groups {
				groups[g] = append(groups[g], o.b)
			}
		}
		// Package membership asks who holds a live allowance, even if a funded
		// pool currently has no paid-node groups to cover.
		for _, b := range bs[uid] {
			if !countsOnly && (scope == "" || scope == "package") && (scope == "" || b.PackageID == target) && b.Kind != KindFree && b.Status != "queued" && b.Active(now) {
				packages[b.PackageID] = append(packages[b.PackageID], b)
			}
		}
		for p, b := range packages {
			out.Packages[p] = append(out.Packages[p], AudienceUser{uid, users[uid].Username, b})
		}
		byNode := map[int64]map[int64]*Bucket{}
		byGroup := map[int64]map[int64]*Bucket{}
		byServer := map[int64]map[int64]*Bucket{}
		add := func(dst map[int64]map[int64]*Bucket, id int64, b []*Bucket) {
			if dst[id] == nil {
				dst[id] = map[int64]*Bucket{}
			}
			for _, v := range b {
				dst[id][v.ID] = v
			}
		}
		// Walk only nodes in this user's entitled groups, rather than every node
		// on the panel. Counts retain integers only, not user×node detail rows.
		if _, ok := groups[free]; free > 0 && !ok {
			groups[free] = nil
		}
		seenNodes, seenGroups := map[int64]bool{}, map[int64]bool{}
		for group, b := range groups {
			for _, n := range nodes[group] {
				if b == nil && !n.external {
					continue
				}
				if countsOnly {
					seenNodes[n.id] = true
					seenGroups[n.group] = true
					continue
				}
				if scope == "" || scope == "node" {
					add(byNode, n.id, b)
				}
				if scope == "" || scope == "group" {
					add(byGroup, n.group, b)
				}
				if !n.external && n.server >= 0 && (scope == "" || scope == "server") {
					add(byServer, n.server, b)
				}
			}
		}
		for id := range seenNodes {
			out.NodeCounts[id]++
		}
		for id := range seenGroups {
			out.GroupCounts[id]++
		}
		appendUsers := func(dst map[int64][]AudienceUser, src map[int64]map[int64]*Bucket) {
			for id, b := range src {
				list := make([]*Bucket, 0, len(b))
				for _, v := range b {
					list = append(list, v)
				}
				sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
				dst[id] = append(dst[id], AudienceUser{uid, users[uid].Username, list})
			}
		}
		appendUsers(out.Nodes, byNode)
		appendUsers(out.Groups, byGroup)
		appendUsers(out.Servers, byServer)
	}
	return out, nil
}
