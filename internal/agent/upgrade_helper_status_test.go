package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"404-probe/internal/protocol"
	"404-probe/internal/updater"
)

func TestUpgradeSuccessWaitsForManagementHelperResult(t *testing.T) {
	updates := make(chan protocol.UpgradeStatusRequest, 2)
	release := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var update protocol.UpgradeStatusRequest
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil || update.Validate() != nil {
			t.Errorf("invalid upgrade status: %+v, %v", update, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		updates <- update
	}))
	defer release.Close()
	client := upgradeHTTPClient{baseURL: release.URL, client: release.Client()}
	operation := protocol.UpgradeOperation{OperationID: strings.Repeat("1", 32), TargetVersion: "v1.4.1", Status: "health_check"}
	local := updater.State{Status: "succeeded"}
	if err := syncUpgradeStatus(t.Context(), client, operation, local); err != nil || len(updates) != 0 {
		t.Fatalf("premature result: %d updates, %v", len(updates), err)
	}
	local.HelperUpdateComplete = true
	local.FailureMessage = protocol.UpgradeHelperWarningPrefix + "下载 install.sh 失败"
	if err := syncUpgradeStatus(t.Context(), client, operation, local); err != nil || len(updates) != 1 {
		t.Fatalf("missing result: %d updates, %v", len(updates), err)
	}
	if got := <-updates; got.Status != "succeeded" || got.FailureCode != "" || got.FailureMessage != local.FailureMessage {
		t.Fatal(got)
	}
}
