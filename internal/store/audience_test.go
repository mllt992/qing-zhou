package store

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCurrentAudienceCanonicalEligibility(t *testing.T) {
	st := newRefundStore(t)
	g, _ := st.CreateGroup(NodeGroup{Name: "paid"})
	g2, _ := st.CreateGroup(NodeGroup{Name: "second"})
	p, _ := st.CreatePackage(Package{Type: "plan", Name: "plan", TrafficBytes: giB, DurationDays: 30, Enabled: true, Stock: -1})
	if err := st.SetPlanGroups(p, []int64{g, g2}); err != nil {
		t.Fatal(err)
	}
	pkg, _ := st.GetPackage(p)
	node, err := st.CreateNode(Node{Type: "external", Name: "node", Enabled: true, GroupIDs: []int64{g, g2}})
	if err != nil {
		t.Fatal(err)
	}
	disabled, _ := st.CreateNode(Node{Type: "external", Name: "off", Enabled: false, GroupIDs: []int64{g}})
	ids := map[string]int64{}
	for _, name := range []string{"active", "queued", "expired", "empty", "fallback", "banned", "zero", "gift"} {
		id := mkUser(t, st, name)
		ids[name] = id
		if _, err = st.AssignPackage(id, pkg, 0, noopSync); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := st.db.Exec(q, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`UPDATE user_plans SET status='queued' WHERE user_id=? AND package_id=?`, ids["queued"], p)
	exec(`UPDATE user_plans SET expiry_at=? WHERE user_id=? AND package_id=?`, time.Now().Unix()-100, ids["expired"], p)
	for _, n := range []string{"empty", "fallback", "gift"} {
		exec(`UPDATE user_plans SET used_up=traffic_limit WHERE user_id=? AND package_id=?`, ids[n], p)
	}
	exec(`UPDATE user_plans SET traffic_limit=0 WHERE user_id=? AND package_id=?`, ids["zero"], p)
	exec(`UPDATE users SET status='banned' WHERE id=?`, ids["banned"])
	for _, n := range []string{"fallback", "zero"} {
		if err = st.EnsurePoolBucket(ids[n], n+"-pool", "uuid", "secret"); err != nil {
			t.Fatal(err)
		}
		exec(`UPDATE user_plans SET traffic_limit=? WHERE user_id=? AND kind='pool'`, giB, ids[n])
	}
	exec(`INSERT INTO user_plans(user_id,kind,package_id,name,client_name,client_uuid,client_secret,traffic_limit,created_at,updated_at) VALUES(?,'plan',0,'gift','gift-client','uuid','secret',?,0,0)`, ids["gift"], giB)
	snap, err := st.CurrentAudience()
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Nodes[node]) != 3 {
		t.Fatalf("got audience %#v", snap.Nodes[node])
	}
	if len(snap.Groups[g]) != 3 || len(snap.Groups[g2]) != 3 || len(snap.Nodes[disabled]) != 0 {
		t.Fatal("groups/disabled count mismatch")
	}
	want := map[int64]bool{ids["active"]: true, ids["fallback"]: true, ids["gift"]: true}
	for _, u := range snap.Nodes[node] {
		if !want[u.UserID] {
			t.Fatalf("unexpected user %d", u.UserID)
		}
		delete(want, u.UserID)
		for _, b := range u.Buckets {
			if b.Status == "queued" || !b.Active(time.Now().Unix()) {
				t.Fatal("inactive allowance returned")
			}
		}
	}
	if len(want) != 0 {
		t.Fatal("eligible zero-use user missing")
	}
	encoded, _ := json.Marshal(snap.Nodes[node])
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "client_uuid") {
		t.Fatal("credential leak")
	}
	// Active membership remains distinct from historical usage, including zero use.
	if len(snap.Packages[p]) != 1 || snap.Packages[p][0].UserID != ids["active"] {
		t.Fatalf("active package members %#v", snap.Packages[p])
	}
}
func TestCurrentAudienceFreeFallbackAndNoTruncation(t *testing.T) {
	st := newRefundStore(t)
	g, _ := st.CreateGroup(NodeGroup{Name: "free"})
	if err := st.SetSetting("free_group_id", strconv.FormatInt(g, 10)); err != nil {
		t.Fatal(err)
	}
	n, _ := st.CreateNode(Node{Type: "external", Enabled: true, GroupIDs: []int64{g}})
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1050; i++ {
		if _, err = tx.Exec(`INSERT INTO users(username,password_hash,created_at,updated_at) VALUES(?,'x',0,0)`, strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	snap, err := st.CurrentAudience()
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Nodes[n]) != 1050 {
		t.Fatalf("truncated audience %d", len(snap.Nodes[n]))
	}
}

func TestAudienceScopedAndCountsMatch(t *testing.T) {
	st := newRefundStore(t)
	g, _ := st.CreateGroup(NodeGroup{Name: "g"})
	p, _ := st.CreatePackage(Package{Type: "plan", Name: "p", TrafficBytes: giB, DurationDays: 30, Enabled: true, Stock: -1})
	st.SetPlanGroups(p, []int64{g})
	pkg, _ := st.GetPackage(p)
	uid := mkUser(t, st, "member")
	if _, err := st.AssignPackage(uid, pkg, 0, noopSync); err != nil {
		t.Fatal(err)
	}
	n, _ := st.CreateNode(Node{Type: "external", Enabled: true, GroupIDs: []int64{g}})
	count, err := st.AudienceCounts()
	if err != nil {
		t.Fatal(err)
	}
	if count.NodeCounts[n] != 1 || count.GroupCounts[g] != 1 || len(count.Nodes) != 0 || len(count.Packages) != 0 {
		t.Fatal("counts allocated detail or lost members")
	}
	for _, scope := range []string{"node", "group", "package"} {
		id := n
		if scope == "group" {
			id = g
		}
		if scope == "package" {
			id = p
		}
		s, err := st.ScopedAudience(scope, id)
		if err != nil {
			t.Fatal(err)
		}
		switch scope {
		case "node":
			if len(s.Nodes[n]) != 1 || len(s.Groups) != 0 || len(s.Packages) != 0 {
				t.Fatal("node scope leak")
			}
		case "group":
			if len(s.Groups[g]) != 1 || len(s.Nodes) != 0 {
				t.Fatal("group scope leak")
			}
		case "package":
			if len(s.Packages[p]) != 1 || len(s.Nodes) != 0 {
				t.Fatal("package scope leak")
			}
		}
	}
}

func BenchmarkAudience3000Users200Nodes(b *testing.B) {
	st, err := Open(b.TempDir() + "/benchmark.db")
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()
	if err = st.Migrate(); err != nil {
		b.Fatal(err)
	}
	tx, err := st.db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	run := func(q string, a ...any) {
		if _, err = tx.Exec(q, a...); err != nil {
			b.Fatal(err)
		}
	}
	for g := 1; g <= 20; g++ {
		run(`INSERT INTO node_groups(id,name,created_at) VALUES(?,?,0)`, g, "group")
		run(`INSERT INTO plan_groups(package_id,group_id) VALUES(?,?)`, g, g)
	}
	for n := 1; n <= 200; n++ {
		run(`INSERT INTO nodes(id,type,name,enabled,created_at) VALUES(?,'external','node',1,0)`, n)
		run(`INSERT INTO node_group_members(node_id,group_id) VALUES(?,?)`, n, 1+(n-1)%20)
	}
	for u := 1; u <= 3000; u++ {
		run(`INSERT INTO users(id,username,password_hash,created_at,updated_at) VALUES(?,?,'x',0,0)`, u, strconv.Itoa(u))
		for k := 0; k < 2; k++ {
			run(`INSERT INTO user_plans(user_id,kind,package_id,name,client_name,traffic_limit,created_at,updated_at) VALUES(?,'plan',?,'plan',?,100000,0,0)`, u, 1+(u+k)%20, strconv.Itoa(u)+"-"+strconv.Itoa(k))
		}
	}
	if err = tx.Commit(); err != nil {
		b.Fatal(err)
	}
	for _, mode := range []string{"counts", "one-node"} {
		b.Run(mode, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var err error
				if mode == "counts" {
					_, err = st.AudienceCounts()
				} else {
					_, err = st.ScopedAudience("node", 1)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestAudienceSelfBuiltAndBrokenRoutes(t *testing.T) {
	st := newRefundStore(t)
	g, _ := st.CreateGroup(NodeGroup{Name: "paid"})
	p, _ := st.CreatePackage(Package{Type: "plan", Name: "p", TrafficBytes: giB, DurationDays: 30, Enabled: true, Stock: -1})
	st.SetPlanGroups(p, []int64{g})
	pkg, _ := st.GetPackage(p)
	uid := mkUser(t, st, "selfbuilt-member")
	if _, err := st.AssignPackage(uid, pkg, 0, noopSync); err != nil {
		t.Fatal(err)
	}
	for _, i := range []struct {
		id      int
		enabled bool
		next    int
	}{{1, true, 0}, {2, false, 0}, {3, true, 4}, {4, true, 3}, {5, true, 0}} {
		if _, err := st.db.Exec(`INSERT INTO sb_inbounds(id,server_id,type,tag,listen_port,enabled,upstream_inbound_id,created_at,updated_at) VALUES(?,?,'vless',?,1234,?,?,0,0)`, i.id, 10+i.id, strconv.Itoa(i.id), i.enabled, i.next); err != nil {
			t.Fatal(err)
		}
	}
	nodes := map[string]int64{}
	for _, n := range []Node{{Name: "direct", InboundTag: "1"}, {Name: "valid-route", InboundTag: "1", RouteUpstreamInboundID: 5}, {Name: "disabled", InboundTag: "2"}, {Name: "missing", InboundTag: "absent"}, {Name: "broken", InboundTag: "1", RouteUpstreamBroken: true}, {Name: "disabled-exit", InboundTag: "1", RouteUpstreamInboundID: 2}, {Name: "cycle", InboundTag: "1", RouteUpstreamInboundID: 3}, {Name: "missing-exit", InboundTag: "1", RouteUpstreamInboundID: 99}} {
		n.Type = "self_built"
		n.Enabled = true
		n.GroupIDs = []int64{g}
		id, err := st.CreateNode(n)
		if err != nil {
			t.Fatal(err)
		}
		nodes[n.Name] = id
	}
	snapshot, err := st.CurrentAudience()
	if err != nil {
		t.Fatal(err)
	}
	for name, id := range nodes {
		want := 0
		if name == "direct" || name == "valid-route" {
			want = 1
		}
		if len(snapshot.Nodes[id]) != want {
			t.Fatalf("%s audience=%d", name, len(snapshot.Nodes[id]))
		}
	}
	if len(snapshot.Servers[11]) != 1 || len(snapshot.Groups[g]) != 1 {
		t.Fatal("shared entry duplicated user")
	}
	server, err := st.ScopedAudience("server", 11)
	if err != nil {
		t.Fatal(err)
	}
	if len(server.Servers[11]) != 1 || len(server.Nodes) != 0 {
		t.Fatal("machine scope incorrect")
	}
}

func TestAudienceEmailAdmissionAndLocalServer(t *testing.T) {
	st := newRefundStore(t)
	g, _ := st.CreateGroup(NodeGroup{Name: "free"})
	st.SetSetting("free_group_id", strconv.FormatInt(g, 10))
	st.SetSetting("email_verify_required", "true")
	ext, _ := st.CreateNode(Node{Type: "external", Enabled: true, GroupIDs: []int64{g}})
	if _, err := st.db.Exec(`INSERT INTO sb_inbounds(server_id,type,tag,listen_port,created_at,updated_at) VALUES(0,'vless','local',1234,0,0)`); err != nil {
		t.Fatal(err)
	}
	local, _ := st.CreateNode(Node{Type: "self_built", InboundTag: "local", Enabled: true, GroupIDs: []int64{g}})
	ids := map[string]int64{}
	for _, name := range []string{"pending", "welcome", "verified", "exempt", "admin", "paid-queued", "paid-expired"} {
		uid := mkUser(t, st, name)
		ids[name] = uid
		if _, err := st.db.Exec(`UPDATE users SET email_gate_exempt=0,email_verified=0 WHERE id=?`, uid); err != nil {
			t.Fatal(err)
		}
		if err := st.EnsureFreeBucket(uid, name); err != nil {
			t.Fatal(err)
		}
	}
	st.db.Exec(`UPDATE users SET email_verified=1 WHERE id=?`, ids["verified"])
	st.db.Exec(`UPDATE users SET email_gate_exempt=1 WHERE id=?`, ids["exempt"])
	st.db.Exec(`UPDATE users SET role='admin' WHERE id=?`, ids["admin"])
	for _, tc := range []struct {
		name   string
		pkg    int
		status string
		expiry int64
	}{{"welcome", -1, "active", 0}, {"paid-queued", 1, "queued", 0}, {"paid-expired", 1, "active", time.Now().Unix() - 1}} {
		if _, err := st.db.Exec(`INSERT INTO user_plans(user_id,kind,package_id,status,expiry_at,client_name,traffic_limit,created_at,updated_at) VALUES(?,'plan',?,?,?,?,1000,0,0)`, ids[tc.name], tc.pkg, tc.status, tc.expiry, tc.name+"-bucket"); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := st.CurrentAudience()
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range []int64{ext, local} {
		if len(snapshot.Nodes[node]) != 4 {
			t.Fatalf("email gate count=%d", len(snapshot.Nodes[node]))
		}
		for _, u := range snapshot.Nodes[node] {
			if u.UserID == ids["pending"] || u.UserID == ids["welcome"] || u.UserID == ids["paid-expired"] {
				t.Fatal("unverified account admitted")
			}
		}
	}
	if len(snapshot.Servers[0]) != 4 {
		t.Fatalf("panel-local source lost: %d", len(snapshot.Servers[0]))
	}
	scoped, err := st.ScopedAudience("server", 0)
	if err != nil || len(scoped.Servers[0]) != 4 {
		t.Fatal("local scope mismatch", err)
	}
	// External-only entitlement must never become panel-local machine access.
	if _, err = st.db.Exec(`UPDATE nodes SET enabled=0 WHERE id=?`, local); err != nil {
		t.Fatal(err)
	}
	scoped, err = st.ScopedAudience("server", 0)
	if err != nil || len(scoped.Servers[0]) != 0 {
		t.Fatal("external node attributed to panel", err)
	}
	st.SetSetting("email_verify_required", "false")
	counts, err := st.AudienceCounts()
	if err != nil || counts.NodeCounts[ext] != 7 {
		t.Fatal("disabled email gate still blocks", err)
	}
}
