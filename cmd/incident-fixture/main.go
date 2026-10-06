package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type record struct {
	Schema         string `json:"schema"`
	Stage          string `json:"stage"`
	Attempt        string `json:"attempt"`
	BindingVersion string `json:"binding_version"`
	UpstreamUID    string `json:"upstream_uid"`
	RequestSHA256  string `json:"request_sha256"`
	Status         int    `json:"status,omitempty"`
	ResponseSHA256 string `json:"response_sha256,omitempty"`
}

type locatorRecord struct {
	Schema    string `json:"schema"`
	Attempt   string `json:"attempt"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
	Relation  string `json:"relation"`
}

func hash(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }

func emit(stage, attempt, uid, requestHash string, status int, responseHash string) {
	encoded, _ := json.Marshal(record{Schema: "northstar.request.v1", Stage: stage, Attempt: attempt, BindingVersion: "v1", UpstreamUID: uid, RequestSHA256: requestHash, Status: status, ResponseSHA256: responseHash})
	fmt.Println(string(encoded))
}

func emitLocator(attempt, namespace, name, uid string) {
	encoded, _ := json.Marshal(locatorRecord{Schema: "northstar.dependency-locator.v1", Attempt: attempt, Kind: "Pod", Namespace: namespace, Name: name, UID: uid, Relation: "runtime_dependency"})
	fmt.Println(string(encoded))
}

func setIdentityHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Pod-UID", os.Getenv("POD_UID"))
	w.Header().Set("X-Pod-Name", os.Getenv("POD_NAME"))
	w.Header().Set("X-Pod-Namespace", os.Getenv("POD_NAMESPACE"))
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--emit" {
		runEmitter()
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	if os.Getenv("ROLE") == "upstream" {
		mux.HandleFunc("/identity", func(w http.ResponseWriter, _ *http.Request) {
			setIdentityHeaders(w)
			w.WriteHeader(http.StatusOK)
		})
		mux.HandleFunc("/fail", func(w http.ResponseWriter, r *http.Request) {
			uid := os.Getenv("POD_UID")
			requestBody, err := io.ReadAll(io.LimitReader(r.Body, 1024))
			if err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			attempt := r.Header.Get("X-Attempt")
			requestHash := hash(requestBody)
			responseBody, status := []byte("upstream failure"), http.StatusServiceUnavailable
			if os.Getenv("FAIL") == "false" {
				responseBody, status = []byte("healthy"), http.StatusOK
			}
			if status >= 400 {
				emit("receiver_accepted", attempt, uid, requestHash, 0, "")
			}
			setIdentityHeaders(w)
			w.WriteHeader(status)
			_, _ = w.Write(responseBody)
			if status >= 400 {
				emit("receiver_responded", attempt, uid, requestHash, status, hash(responseBody))
			}
		})
	} else {
		mux.HandleFunc("/attempt", caller)
	}
	server := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(server.ListenAndServe())
}

func caller(w http.ResponseWriter, r *http.Request) {
	upstream := os.Getenv("UPSTREAM_URL")
	attempt := r.Header.Get("X-Attempt")
	variant := r.URL.Query().Get("variant")
	if len(attempt) != 32 || (variant != "bridge" && variant != "no-bridge") {
		http.Error(w, "invalid attempt", http.StatusBadRequest)
		return
	}
	client := &http.Client{Timeout: 5 * time.Second}
	requestBody := []byte("k8os incident request")
	var uid string
	var name, namespace string
	var status int
	var responseBody []byte
	if variant == "no-bridge" {
		identity, err := client.Get(upstream + "/identity")
		if err != nil {
			http.Error(w, "upstream identity unavailable", http.StatusBadGateway)
			return
		}
		uid = identity.Header.Get("X-Pod-UID")
		name, namespace = identity.Header.Get("X-Pod-Name"), identity.Header.Get("X-Pod-Namespace")
		_ = identity.Body.Close()
		responseBody, status = []byte("local dependency failure"), http.StatusServiceUnavailable
	} else {
		request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstream+"/fail", strings.NewReader(string(requestBody)))
		if err != nil {
			http.Error(w, "invalid request", http.StatusInternalServerError)
			return
		}
		request.Header.Set("X-Attempt", attempt)
		response, err := client.Do(request)
		if err != nil {
			http.Error(w, "upstream request failed", http.StatusBadGateway)
			return
		}
		uid, status = response.Header.Get("X-Pod-UID"), response.StatusCode
		name, namespace = response.Header.Get("X-Pod-Name"), response.Header.Get("X-Pod-Namespace")
		responseBody, err = io.ReadAll(io.LimitReader(response.Body, 1024))
		_ = response.Body.Close()
		if err != nil {
			http.Error(w, "upstream response failed", http.StatusBadGateway)
			return
		}
	}
	if uid == "" || name == "" || namespace == "" {
		http.Error(w, "missing upstream identity", http.StatusBadGateway)
		return
	}
	if status >= 400 {
		emitLocator(attempt, namespace, name, uid)
		emit("caller_returned", attempt, uid, hash(requestBody), status, hash(responseBody))
	}
	w.WriteHeader(status)
	_, _ = w.Write(responseBody)
}

func runEmitter() {
	attempt, variant := os.Getenv("ATTEMPT"), os.Getenv("VARIANT")
	request, err := http.NewRequest(http.MethodGet, os.Getenv("CALLER_URL")+"/attempt?variant="+variant, nil)
	if err != nil {
		log.Fatal(err)
	}
	request.Header.Set("X-Attempt", attempt)
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		log.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
	expected, err := strconv.Atoi(os.Getenv("EXPECTED_STATUS"))
	if err != nil {
		log.Fatal(err)
	}
	if response.StatusCode != expected {
		log.Fatalf("unexpected response status %d; expected %d", response.StatusCode, expected)
	}
	fmt.Printf("attempt=%s variant=%s status=%d\n", attempt, variant, response.StatusCode)
}
