package main

import (
	"bufio"
	"bytes"
	"codeswitch/services"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type forwardedRequest struct {
	Index              int    `json:"index"`
	PreviousResponseID string `json:"previous_response_id"`
	Store              bool   `json:"store"`
	InputCount         int64  `json:"input_count"`
	InputLastContent   string `json:"input_last_content"`
	Instructions       string `json:"instructions"`
	ToolsCount         int64  `json:"tools_count"`
	SessionHeader      string `json:"session_header"`
}

type responseSummary struct {
	StatusCode         int    `json:"status_code"`
	SessionHeader      string `json:"session_header"`
	ResponseID         string `json:"response_id"`
	PreviousResponseID string `json:"previous_response_id"`
	OutputText         string `json:"output_text"`
}

type smokeResult struct {
	TempHome  string             `json:"temp_home"`
	RelayAddr string             `json:"relay_addr"`
	Upstream  string             `json:"upstream_addr"`
	First     responseSummary    `json:"first_response"`
	Second    responseSummary    `json:"second_response"`
	Forwarded []forwardedRequest `json:"forwarded_requests"`
}

func main() {
	log.SetFlags(0)

	tempHome, err := os.MkdirTemp("", "codeswitch-relay-stream-smoke-*")
	if err != nil {
		log.Fatalf("create temp home: %v", err)
	}
	defer os.RemoveAll(tempHome)

	setTempHome(tempHome)

	gin.SetMode(gin.ReleaseMode)

	if err := services.InitDatabase(); err != nil {
		log.Fatalf("init database: %v", err)
	}
	if err := services.InitGlobalDBQueue(); err != nil {
		log.Fatalf("init db queue: %v", err)
	}

	upstreamAddr := "127.0.0.1:19101"
	relayAddr := "127.0.0.1:19100"

	var (
		mu        sync.Mutex
		forwarded []forwardedRequest
	)

	upstreamServer := &http.Server{
		Addr: upstreamAddr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bodyBytes, readErr := io.ReadAll(r.Body)
			if readErr != nil {
				http.Error(w, readErr.Error(), http.StatusInternalServerError)
				return
			}

			mu.Lock()
			index := len(forwarded) + 1
			entry := forwardedRequest{
				Index:              index,
				PreviousResponseID: strings.TrimSpace(gjson.GetBytes(bodyBytes, "previous_response_id").String()),
				Store:              gjson.GetBytes(bodyBytes, "store").Bool(),
				InputCount:         gjson.GetBytes(bodyBytes, "input.#").Int(),
				InputLastContent:   gjson.GetBytes(bodyBytes, "input.0.content").String(),
				Instructions:       gjson.GetBytes(bodyBytes, "instructions").String(),
				ToolsCount:         gjson.GetBytes(bodyBytes, "tools.#").Int(),
				SessionHeader:      r.Header.Get("X-CodeSwitch-Session-Key"),
			}
			forwarded = append(forwarded, entry)
			mu.Unlock()

			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)

			responseID := fmt.Sprintf("resp_stream_%d", index)
			outputText := "READY"
			if index > 1 {
				outputText = "ORANGE-42"
			}

			writeSSE(w, map[string]any{
				"type": "response.created",
				"response": map[string]any{
					"id":                   responseID,
					"previous_response_id": entry.PreviousResponseID,
					"store":                entry.Store,
				},
			})
			writeSSE(w, map[string]any{
				"type":  "response.output_text.delta",
				"delta": outputText,
			})
			writeSSE(w, map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     responseID,
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  12,
						"output_tokens": 0,
					},
				},
			})
		}),
	}

	upstreamLn, err := net.Listen("tcp", upstreamAddr)
	if err != nil {
		log.Fatalf("listen upstream %s: %v", upstreamAddr, err)
	}
	defer upstreamLn.Close()
	go func() {
		if serveErr := upstreamServer.Serve(upstreamLn); serveErr != nil && serveErr != http.ErrServerClosed {
			log.Printf("upstream serve error: %v", serveErr)
		}
	}()
	defer upstreamServer.Close()

	providerService := services.NewProviderService()
	settingsService := services.NewSettingsService()
	blacklistService := services.NewBlacklistService(settingsService, nil)
	geminiService := services.NewGeminiService(relayAddr)
	relay := services.NewProviderRelayService(providerService, geminiService, blacklistService, nil, relayAddr)
	defer relay.Stop()

	if err := providerService.SaveProviders("codex", []services.Provider{
		{
			ID:      1,
			Name:    "smoke-upstream",
			APIURL:  "http://" + upstreamAddr,
			APIKey:  "test-api-key",
			Enabled: true,
			Level:   1,
		},
	}); err != nil {
		log.Fatalf("save provider config: %v", err)
	}

	if err := relay.Start(); err != nil {
		log.Fatalf("start relay: %v", err)
	}
	if err := waitHTTP(relayAddr, 5*time.Second); err != nil {
		log.Fatalf("wait relay ready: %v", err)
	}

	firstResp, err := sendStreamingRequest(
		relayAddr,
		"stream-smoke-1",
		`{"model":"gpt-5.1","stream":true,"instructions":"system","tools":[{"type":"function","name":"tool_a"}],"input":[{"role":"user","content":"hello"}]}`,
	)
	if err != nil {
		log.Fatalf("first streaming request: %v", err)
	}

	secondResp, err := sendStreamingRequest(
		relayAddr,
		"stream-smoke-1",
		`{"model":"gpt-5.1","stream":true,"input":[{"role":"user","content":"hello"},{"role":"user","content":"world"}]}`,
	)
	if err != nil {
		log.Fatalf("second streaming request: %v", err)
	}

	mu.Lock()
	result := smokeResult{
		TempHome:  tempHome,
		RelayAddr: relayAddr,
		Upstream:  upstreamAddr,
		First:     firstResp,
		Second:    secondResp,
		Forwarded: append([]forwardedRequest(nil), forwarded...),
	}
	mu.Unlock()

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		log.Fatalf("encode result: %v", err)
	}
}

func setTempHome(tempHome string) {
	_ = os.Setenv("HOME", tempHome)
	_ = os.Setenv("USERPROFILE", tempHome)
	if runtime.GOOS == "windows" {
		volumeName := filepath.VolumeName(tempHome)
		rest := strings.TrimPrefix(tempHome, volumeName)
		if volumeName != "" {
			_ = os.Setenv("HOMEDRIVE", volumeName)
		}
		if rest != "" {
			_ = os.Setenv("HOMEPATH", rest)
		}
	}
}

func writeSSE(w http.ResponseWriter, payload map[string]any) {
	data, _ := json.Marshal(payload)
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(data)
	_, _ = w.Write([]byte("\n\n"))
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func waitHTTP(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", addr)
}

func sendStreamingRequest(addr string, sessionKey string, body string) (responseSummary, error) {
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/responses", bytes.NewBufferString(body))
	if err != nil {
		return responseSummary{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-CodeSwitch-Session-Key", sessionKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return responseSummary{}, err
	}
	defer resp.Body.Close()

	summary := responseSummary{
		StatusCode:    resp.StatusCode,
		SessionHeader: resp.Header.Get("X-CodeSwitch-Session-Key"),
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" || !gjson.Valid(payload) {
			continue
		}
		eventType := gjson.Get(payload, "type").String()
		switch eventType {
		case "response.created":
			summary.ResponseID = gjson.Get(payload, "response.id").String()
			summary.PreviousResponseID = gjson.Get(payload, "response.previous_response_id").String()
		case "response.output_text.delta":
			summary.OutputText += gjson.Get(payload, "delta").String()
		}
	}
	if err := scanner.Err(); err != nil {
		return summary, err
	}

	return summary, nil
}
