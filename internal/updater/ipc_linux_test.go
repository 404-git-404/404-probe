//go:build linux

package updater

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestClientHalfClosesRequestBeforeReadingResponse(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "updater.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	serverDone := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer connection.Close()
		body, readErr := io.ReadAll(connection)
		if readErr != nil {
			serverDone <- readErr
			return
		}
		var request Request
		if decodeErr := json.Unmarshal(body, &request); decodeErr != nil {
			serverDone <- decodeErr
			return
		}
		serverDone <- json.NewEncoder(connection).Encode(Response{Accepted: true, State: State{
			OperationID: request.OperationID, TargetVersion: request.TargetVersion, Status: "claimed",
		}})
	}()

	client := Client{Socket: socket, Timeout: time.Second}
	response, err := client.Call(context.Background(), Request{
		ProtocolVersion: ProtocolVersion, Action: ActionStart,
		OperationID: "0123456789abcdef0123456789abcdef", TargetVersion: "v0.8.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Accepted || response.State.Status != "claimed" {
		t.Fatalf("unexpected response %+v", response)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}
