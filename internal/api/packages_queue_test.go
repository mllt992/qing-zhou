package api

import (
	"strings"
	"testing"

	"qingzhou/internal/store"
)

func TestValidatePackageQueueKey(t *testing.T) {
	base := store.Package{Type: "plan", Name: "B", DurationDays: 30, TrafficBytes: 1 << 30, QueueKey: " standard-b "}
	if msg := validatePackage(&base); msg != "" {
		t.Fatalf("valid queue key rejected: %s", msg)
	}
	if base.QueueKey != "standard-b" {
		t.Fatalf("queue key was not normalized: %q", base.QueueKey)
	}

	bad := base
	bad.QueueKey = "有 空格"
	if msg := validatePackage(&bad); !strings.Contains(msg, "续期组") {
		t.Fatalf("invalid queue key error = %q", msg)
	}

	traffic := store.Package{Type: "traffic", Name: "流量包", TrafficBytes: 1 << 30, QueueKey: "standard-b"}
	if msg := validatePackage(&traffic); !strings.Contains(msg, "只有订阅计划") {
		t.Fatalf("traffic queue key error = %q", msg)
	}
}

func TestValidatePackageRejectsZeroTraffic(t *testing.T) {
	p := store.Package{Type: "plan", Name: "零额度", DurationDays: 30}
	if msg := validatePackage(&p); !strings.Contains(msg, "大于 0") {
		t.Fatalf("zero package traffic error = %q", msg)
	}
	p.TrafficBytes = 1 << 30
	p.Options = []store.PlanOption{{Days: 30, TrafficBytes: 0}}
	if msg := validateOptions(&p); !strings.Contains(msg, "大于 0") {
		t.Fatalf("zero option traffic error = %q", msg)
	}
}
