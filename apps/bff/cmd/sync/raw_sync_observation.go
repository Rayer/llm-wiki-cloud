package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

const maxRemoteDiagnosticIDBytes = 100

type rawSyncObservationStage string

const (
	rawSyncStageInventory rawSyncObservationStage = "inventory_scan_hash"
	rawSyncStageRemote    rawSyncObservationStage = "remote_inventory"
	rawSyncStageDiff      rawSyncObservationStage = "diff_validate"
	rawSyncStageUpload    rawSyncObservationStage = "upload"
	rawSyncStageReadback  rawSyncObservationStage = "verification_readback"
	rawSyncStageDownload  rawSyncObservationStage = "download"
)

var rawSyncObservationStageOrder = [...]rawSyncObservationStage{
	rawSyncStageInventory,
	rawSyncStageRemote,
	rawSyncStageDiff,
	rawSyncStageUpload,
	rawSyncStageReadback,
	rawSyncStageDownload,
}

type rawSyncObservedStage struct {
	calls   int64
	errors  int64
	elapsed time.Duration
}

type rawSyncObservation struct {
	mu      sync.Mutex
	started time.Time
	writer  io.Writer
	stages  map[rawSyncObservationStage]rawSyncObservedStage

	authRequests       int64
	syncRequests       int64
	remoteListCalls    int64
	remoteListPages    int64
	refreshRequests    int64
	uploadAttempts     int64
	downloadAttempts   int64
	readbackCalls      int64
	inventoryPasses    int64
	inventoryFiles     int64
	inventoryHashBytes int64
	verifyHashes       int64
	verifyHashBytes    int64
	comparedFiles      int64

	added, updated, skipped, downloaded, failed int
	countsKnown                                 bool
	partial                                     bool
	diagnosticID                                string
}

func newRawSyncObservation() *rawSyncObservation {
	return &rawSyncObservation{
		started: time.Now(),
		writer:  os.Stderr,
		stages:  make(map[rawSyncObservationStage]rawSyncObservedStage),
	}
}

func (o *rawSyncObservation) measure(stage rawSyncObservationStage, operation func() error) error {
	if o == nil {
		return operation()
	}
	started := time.Now()
	err := operation()
	elapsed := time.Since(started)
	o.mu.Lock()
	observed := o.stages[stage]
	observed.calls++
	observed.elapsed += elapsed
	if err != nil {
		observed.errors++
	}
	o.stages[stage] = observed
	o.mu.Unlock()
	return err
}

func (o *rawSyncObservation) scanLocalRawFiles(ctx context.Context, vault string) (*localRawInventory, error) {
	var inventory *localRawInventory
	err := o.measure(rawSyncStageInventory, func() error {
		var scanErr error
		inventory, scanErr = scanLocalRawFilesObserved(ctx, vault, o)
		return scanErr
	})
	return inventory, err
}

func (o *rawSyncObservation) recordAuthRequest(refresh bool) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.authRequests++
	if refresh {
		o.refreshRequests++
	}
	o.mu.Unlock()
}

func (o *rawSyncObservation) recordSyncRequest(method, route string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.syncRequests++
	switch {
	case method == "PUT" && strings.HasPrefix(route, cliRawFileRoute):
		o.uploadAttempts++
	case method == "GET" && strings.HasPrefix(route, cliRawFileRoute):
		o.downloadAttempts++
	}
	o.mu.Unlock()
}

func (o *rawSyncObservation) recordRemoteListPage() {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.remoteListPages++
	o.mu.Unlock()
}

func (o *rawSyncObservation) recordRemoteList(readback bool) {
	if o == nil {
		return
	}
	o.mu.Lock()
	if readback {
		o.readbackCalls++
	} else {
		o.remoteListCalls++
	}
	o.mu.Unlock()
}

func (o *rawSyncObservation) recordInventoryHash(bytes int64) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.inventoryFiles++
	o.inventoryHashBytes += bytes
	o.mu.Unlock()
}

func (o *rawSyncObservation) recordInventoryPass() {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.inventoryPasses++
	o.mu.Unlock()
}

func (o *rawSyncObservation) recordVerificationHash(bytes int64) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.verifyHashes++
	o.verifyHashBytes += bytes
	o.mu.Unlock()
}

func (o *rawSyncObservation) recordComparedFile() {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.comparedFiles++
	o.mu.Unlock()
}

func (o *rawSyncObservation) recordError(err error) {
	if o == nil || err == nil {
		return
	}
	var diagnostic interface{ DiagnosticID() string }
	if errors.As(err, &diagnostic) {
		o.setDiagnosticID(diagnostic.DiagnosticID())
	}
}

func (o *rawSyncObservation) setDiagnosticID(id string) {
	if o == nil || !validRemoteDiagnosticID(id) {
		return
	}
	o.mu.Lock()
	o.diagnosticID = id
	o.mu.Unlock()
}

func (o *rawSyncObservation) setResult(result rawPushResult) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.added = result.added
	o.updated = result.updated
	o.skipped = result.skipped
	o.downloaded = result.downloaded
	o.failed = len(result.failed)
	o.countsKnown = true
	o.partial = len(result.failed) > 0
	o.mu.Unlock()
}

func (o *rawSyncObservation) finish(err error) {
	if o == nil {
		return
	}
	o.mu.Lock()
	started := o.started
	writer := o.writer
	stages := make(map[rawSyncObservationStage]rawSyncObservedStage, len(o.stages))
	for stage, value := range o.stages {
		stages[stage] = value
	}
	values := [...]int64{
		o.authRequests, o.syncRequests, o.remoteListCalls, o.remoteListPages,
		o.refreshRequests, o.uploadAttempts, o.downloadAttempts, o.readbackCalls,
		o.inventoryPasses, o.inventoryFiles, o.inventoryHashBytes,
		o.verifyHashes, o.verifyHashBytes, o.comparedFiles,
	}
	added, updated, skipped, downloaded, failed, countsKnown := o.added, o.updated, o.skipped, o.downloaded, o.failed, o.countsKnown
	partial, diagnosticID := o.partial, o.diagnosticID
	o.mu.Unlock()

	outcome := "error"
	if err == nil {
		outcome = "success"
	} else if partial {
		outcome = "partial"
	}
	var line strings.Builder
	fmt.Fprintf(&line, "lwc-sync observation operation=push outcome=%s wall_ms=%d", outcome, time.Since(started).Milliseconds())
	if countsKnown {
		fmt.Fprintf(&line, " added=%d updated=%d skipped=%d downloaded=%d failed=%d", added, updated, skipped, downloaded, failed)
	}
	fmt.Fprintf(&line, " auth_requests=%d sync_requests=%d remote_list_calls=%d remote_list_pages=%d refresh_requests=%d upload_attempts=%d download_attempts=%d readback_calls=%d inventory_passes=%d inventory_files=%d inventory_hash_bytes=%d verify_hashes=%d verify_hash_bytes=%d compared_files=%d", values[0], values[1], values[2], values[3], values[4], values[5], values[6], values[7], values[8], values[9], values[10], values[11], values[12], values[13])
	for _, stage := range rawSyncObservationStageOrder {
		observed, ok := stages[stage]
		if !ok || observed.calls == 0 {
			continue
		}
		fmt.Fprintf(&line, " %s_calls=%d %s_errors=%d %s_ms=%d", stage, observed.calls, stage, observed.errors, stage, observed.elapsed.Milliseconds())
	}
	if validRemoteDiagnosticID(diagnosticID) {
		fmt.Fprintf(&line, " diagnostic_id=%s", diagnosticID)
	}
	line.WriteByte('\n')
	_, _ = io.WriteString(writer, line.String())
}

func parseRemoteDiagnosticID(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		return ""
	}
	var id string
	if err := json.Unmarshal(payload["diagnostic_id"], &id); err != nil || !validRemoteDiagnosticID(id) {
		return ""
	}
	return id
}

func validRemoteDiagnosticID(id string) bool {
	if len(id) < 8 || len(id) > maxRemoteDiagnosticIDBytes {
		return false
	}
	for i := 0; i < len(id); i++ {
		b := id[i]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-' {
			continue
		}
		return false
	}
	return true
}
