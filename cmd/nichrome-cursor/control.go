package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/xtrcom/xt-nichrome/internal/types"
)

func sendControl(req types.CompanionRequest, pinSeconds int) error {
	const endpoint = "http://127.0.0.1:7798"
	client := http.Client{
		Transport: &http.Transport{},
		Timeout:   3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	var resp *http.Response
	var err error
	switch req.Action {
	case "status":
		resp, err = client.Get(endpoint + "/status")
	case "guide", "move", "say", "show", "hide", "clear":
		if pinSeconds < 1 || pinSeconds > 300 {
			return fmt.Errorf("-pin-seconds must be between 1 and 300")
		}
		body, marshalErr := json.Marshal(struct {
			types.CompanionRequest
			PinSeconds int `json:"pinSeconds"`
		}{req, pinSeconds})
		if marshalErr != nil {
			return marshalErr
		}
		resp, err = client.Post(endpoint+"/guide", "application/json", bytes.NewReader(body))
	default:
		return fmt.Errorf("unknown cursor control %q", req.Action)
	}
	if err != nil {
		return fmt.Errorf("contact the local cursor daemon: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cursor daemon returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var state types.CompanionState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("decode cursor state: %w", err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(state)
}
