package store

import "testing"

func TestNodeGroupAIFlagPersistsAndAggregatesAcrossMemberships(t *testing.T) {
	st := newRefundStore(t)
	ordinary, err := st.CreateGroup(NodeGroup{Name: "普通", SortOrder: 1})
	if err != nil {
		t.Fatal(err)
	}
	aiGroup, err := st.CreateGroup(NodeGroup{Name: "AI", IsAI: true, SortOrder: 2})
	if err != nil {
		t.Fatal(err)
	}
	nodeID, err := st.CreateNode(Node{
		Type: "external", Name: "shared", Remark: "订阅备注", ShareLink: "trojan://pw@example.com:443#shared",
		Enabled: true, GroupIDs: []int64{ordinary, aiGroup},
	})
	if err != nil {
		t.Fatal(err)
	}

	groups, err := st.ListGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].IsAI || !groups[1].IsAI {
		t.Fatalf("AI flags were not persisted: %+v", groups)
	}
	nodes, err := st.NodesInGroupsTagged([]int64{ordinary, aiGroup})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].ID != nodeID || !nodes[0].IsAI {
		t.Fatalf("multi-group AI aggregation = %+v", nodes)
	}
	if nodes[0].Remark != "订阅备注" || nodes[0].ShareLink != "trojan://pw@example.com:443#shared" {
		t.Fatalf("grouped node fields were not preserved: %+v", nodes[0].Node)
	}

	groups[1].IsAI = false
	if err := st.UpdateGroup(*groups[1]); err != nil {
		t.Fatal(err)
	}
	nodes, err = st.NodesInGroupsTagged([]int64{ordinary, aiGroup})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].IsAI {
		t.Fatalf("cleared AI flag still affected node: %+v", nodes)
	}
}

func TestNodeAIRequiresAccessibleAIMembership(t *testing.T) {
	st := newRefundStore(t)
	ordinary, _ := st.CreateGroup(NodeGroup{Name: "可访问"})
	aiGroup, _ := st.CreateGroup(NodeGroup{Name: "未授权 AI", IsAI: true})
	_, err := st.CreateNode(Node{
		Type: "external", Name: "shared", ShareLink: "trojan://pw@example.com:443#shared",
		Enabled: true, GroupIDs: []int64{ordinary, aiGroup},
	})
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := st.NodesInGroupsTagged([]int64{ordinary})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].IsAI {
		t.Fatalf("inaccessible AI group leaked its marker: %+v", nodes)
	}
}
