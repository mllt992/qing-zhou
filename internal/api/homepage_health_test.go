package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHomepageMachineHealthDefaultsHiddenAndPersists(t *testing.T) {
	a, st := newUserEditAPI(t)
	readFlag := func() bool {
		t.Helper()
		w := httptest.NewRecorder()
		a.handleConfig(w, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		var response struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		flag, present := response.Data["homepage_machine_health"].(bool)
		if !present {
			t.Fatalf("missing boolean visibility flag: %s", w.Body.String())
		}
		return flag
	}
	if readFlag() {
		t.Fatal("machine health must be hidden on the homepage until enabled")
	}
	for _, value := range []string{"true", "false"} {
		w := putSettings(a, `{"homepage_machine_health":"`+value+`"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		if got, _ := st.GetSetting("homepage_machine_health"); got != value {
			t.Fatalf("stored flag = %q, want %q", got, value)
		}
		if got := readFlag(); got != (value == "true") {
			t.Fatalf("public config flag = %v after saving %s", got, value)
		}
	}
}
