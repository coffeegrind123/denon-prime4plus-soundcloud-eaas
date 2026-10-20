package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalOnlyHTTPRejectsRemoteClients(t *testing.T) {
	allowed := map[string]struct{}{"127.0.0.1": {}}
	handler := localOnlyHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), allowed)

	request := httptest.NewRequest(http.MethodGet, "/ping", nil)
	request.RemoteAddr = "192.168.4.39:12345"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("remote status = %d", response.Code)
	}

	request.RemoteAddr = "127.0.0.1:12345"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("local status = %d", response.Code)
	}
}

func TestAllowedPeer(t *testing.T) {
	allowed := map[string]struct{}{"192.168.4.90": {}}
	if !allowedPeer(&net.TCPAddr{IP: net.ParseIP("192.168.4.90"), Port: 50000}, allowed) {
		t.Fatal("local alias was rejected")
	}
	if allowedPeer(&net.TCPAddr{IP: net.ParseIP("192.168.4.39"), Port: 50000}, allowed) {
		t.Fatal("remote client was allowed")
	}
}
