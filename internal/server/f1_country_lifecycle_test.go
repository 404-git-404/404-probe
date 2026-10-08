package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

// The handler authenticates before reading the body. This barrier changes the
// real Store lifecycle at precisely that boundary, without production hooks.
type f1LifecycleBody struct {
	io.Reader
	beforeRead func()
}

func TestF1JobAndRemovalLifecycleChangeAfterAuthentication(t *testing.T) {
	for _, entry := range []string{"job-claim", "control-claim", "job-result", "removal-claim", "removal-status"} {
		for _, state := range []string{"disabled", "revoked"} {
			t.Run(entry+"/"+state, func(t *testing.T) {
				app, store, agentID, token := testApp(t)
				defer store.Close()
				defer app.Shutdown()
				ctx := context.Background()
				report := reportFor(agentID, 1)
				report.Management = &protocol.AgentManagementCapabilities{RemoteRemoval: true}
				if _, accepted, _, err := store.ProcessReport(ctx, agentID, report, app.now()); err != nil || !accepted {
					t.Fatalf("report=%t %v", accepted, err)
				}
				url := "/api/v1/agent/jobs/claim"
				claim := validClaimRequest(1, report.SessionID)
				var payload any = claim
				switch entry {
				case "control-claim":
					url = "/api/v1/agent/control/claim"
					payload = protocol.ControlClaimRequest{ProtocolVersion: protocol.ControlProtocolVersion, AgentEpoch: 1, SessionID: report.SessionID}
				case "job-result":
					createHTTPTestJob(t, store, "f1-job", agentID, app.now(), time.Minute)
					job, response := claimHTTPJob(t, app, token, claim)
					if job == nil {
						t.Fatalf("initial claim: %d %s", response.Code, response.Body.String())
					}
					result := validHTTPJobResult(job)
					result.SessionID = report.SessionID
					url = "/api/v1/agent/jobs/" + job.JobID + "/result"
					payload = result
				case "removal-claim", "removal-status":
					url = agentRemovalPathPrefix + "claim"
					payload = protocol.AgentRemovalClaimRequest{ProtocolVersion: protocol.AgentRemovalProtocolVersion, AgentEpoch: 1, SessionID: report.SessionID}
					if entry == "removal-status" {
						id := "abcdefabcdefabcdefabcdefabcdefab"
						if _, _, err := store.CreateAgentRemovalOperation(ctx, agentID, id, app.now()); err != nil {
							t.Fatal(err)
						}
						if delivery, err := store.ClaimAgentRemoval(ctx, agentID, 1, report.SessionID, app.now()); err != nil || delivery == nil {
							t.Fatalf("removal delivery=%v %v", delivery, err)
						}
						url = agentRemovalPathPrefix + id + "/status"
						payload = protocol.AgentRemovalStatusRequest{ProtocolVersion: protocol.AgentRemovalProtocolVersion, AgentEpoch: 1, SessionID: report.SessionID, Status: protocol.AgentRemovalStatusUninstalling}
					}
				}
				data, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				var barrierRan bool
				body := &f1LifecycleBody{Reader: strings.NewReader(string(data)), beforeRead: func() {
					barrierRan = true
					var changed bool
					var err error
					if state == "disabled" {
						changed, err = store.DisableAgent(ctx, agentID, app.now())
					} else {
						changed, err = store.RevokeAgent(ctx, agentID, app.now())
					}
					if err != nil || !changed {
						t.Fatalf("transition=%t %v", changed, err)
					}
				}}
				request := httptest.NewRequest(http.MethodPost, url, nil)
				request.Body = body
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Authorization", "Bearer "+token)
				response := httptest.NewRecorder()
				app.Handler().ServeHTTP(response, request)
				want, code := http.StatusLocked, "agent_disabled"
				if state == "revoked" {
					want, code = http.StatusUnauthorized, "agent_revoked"
				}
				if !barrierRan || response.Code != want || jobErrorCode(t, response) != code {
					t.Fatalf("barrier=%t status=%d want=%d body=%s", barrierRan, response.Code, want, response.Body.String())
				}
			})
		}
	}
}

func (b *f1LifecycleBody) Read(p []byte) (int, error) {
	if b.beforeRead != nil {
		f := b.beforeRead
		b.beforeRead = nil
		f()
	}
	return b.Reader.Read(p)
}

func (*f1LifecycleBody) Close() error { return nil }

func TestF1CountryLifecycleChangeAfterAuthentication(t *testing.T) {
	for _, entry := range []string{"claim", "result"} {
		for _, state := range []string{"disabled", "revoked"} {
			t.Run(entry+"/"+state, func(t *testing.T) {
				app, store, agentID, token := testApp(t)
				defer store.Close()
				defer app.Shutdown()
				ctx := context.Background()
				report := reportFor(agentID, 1)
				report.Management = &protocol.AgentManagementCapabilities{CountryCodeLookup: true}
				if _, accepted, _, err := store.ProcessReport(ctx, agentID, report, app.now()); err != nil || !accepted {
					t.Fatalf("report=%t %v", accepted, err)
				}
				op, err := store.CreateAgentCountryCodeLookup(ctx, agentID, app.now())
				if err != nil {
					t.Fatal(err)
				}
				url := countryCodeLookupAgentPathPrefix + "claim"
				var payload any = protocol.AgentCountryCodeLookupClaimRequest{ProtocolVersion: protocol.CountryCodeLookupProtocolVersion, AgentEpoch: 1, SessionID: report.SessionID}
				if entry == "result" {
					if _, err := store.ClaimAgentCountryCodeLookup(ctx, agentID, 1, report.SessionID, app.now()); err != nil {
						t.Fatal(err)
					}
					url = countryCodeLookupAgentPathPrefix + op.OperationID + "/result"
					payload = protocol.AgentCountryCodeLookupResultRequest{ProtocolVersion: protocol.CountryCodeLookupProtocolVersion, AgentEpoch: 1, SessionID: report.SessionID, CountryCode: "US"}
				}
				data, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				var barrierRan bool
				body := &f1LifecycleBody{Reader: strings.NewReader(string(data)), beforeRead: func() {
					barrierRan = true
					var changed bool
					var err error
					if state == "disabled" {
						changed, err = store.DisableAgent(ctx, agentID, app.now())
					} else {
						changed, err = store.RevokeAgent(ctx, agentID, app.now())
					}
					if err != nil || !changed {
						t.Fatalf("transition=%t %v", changed, err)
					}
				}}
				request := httptest.NewRequest(http.MethodPost, url, nil)
				request.Body = body
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Authorization", "Bearer "+token)
				response := httptest.NewRecorder()
				app.Handler().ServeHTTP(response, request)
				want, code := http.StatusLocked, "agent_disabled"
				if state == "revoked" {
					want, code = http.StatusUnauthorized, "agent_revoked"
				}
				if !barrierRan || response.Code != want || jobErrorCode(t, response) != code {
					t.Fatalf("after-auth barrier=%t status=%d want=%d body=%s", barrierRan, response.Code, want, response.Body.String())
				}
			})
		}
	}
}
