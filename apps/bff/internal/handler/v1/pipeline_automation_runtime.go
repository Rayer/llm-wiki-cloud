package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
	"github.com/rayer/llm-wiki-bff/internal/sourceautomation"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const pipelineAutomationScanPageSize = 100

type pipelineAutomationAdmissionStore interface {
	ReserveQuotaForPipelineAutomation(context.Context, string, string, string, pipelinequota.Limits, time.Time, bool, bool, int, int, int, scopedfirestore.PipelineAutomationAdmission) (pipelinequota.Snapshot, bool, error)
}

func (d *ProfileDispatcher) executePipelineAutomation(ctx context.Context, repo *firestoreProfileRepository, work profileruntime.Work) error {
	if d.Handler == nil || d.Handler.store == nil || d.Handler.firestore == nil || d.Handler.firestore.Raw() == nil {
		return errors.New("pipeline automation storage unavailable")
	}
	settings, err := readPipelineAutomationProject(ctx, repo.client, work.UserID, work.ProjectID)
	if err != nil {
		if errors.Is(err, errProfileProjectNotFound) {
			return nil
		}
		return err
	}
	if !settings.EffectiveEnabled {
		return nil
	}
	newRaw, rawDirty, annotationDirty, err := d.Handler.pendingWorkForProject(ctx, work.UserID, work.ProjectID)
	if err != nil {
		return err
	}
	if newRaw+rawDirty == 0 {
		return d.completeProjectAutomationBacklog(ctx, repo, work, settings.Revision)
	}
	rawStore, ok := d.Handler.store.Scope(work.UserID, work.ProjectID).(storage.RawSyncStore)
	if !ok {
		return errors.New("pipeline automation raw inventory unavailable")
	}
	fingerprint, err := sourceautomation.InventoryFingerprint(ctx, rawStore)
	if err != nil {
		return err
	}
	if fingerprint == "" {
		return d.completeProjectAutomationBacklog(ctx, repo, work, settings.Revision)
	}
	if d.Handler.isDemoUser(work.UserID) {
		return d.completeProjectAutomationBacklog(ctx, repo, work, settings.Revision)
	}
	alreadyRunning, err := d.Handler.isPipelineRunning(ctx, work.UserID, work.ProjectID)
	if err != nil {
		return err
	}
	if alreadyRunning {
		return errProfileRuntimeWaiting
	}
	quota := d.Handler.effectiveQuotaStore()
	autoStore, ok := quota.(pipelineAutomationAdmissionStore)
	if !ok {
		return errors.New("pipeline automation admission unavailable")
	}
	intentStore, ok := quota.(pipelineIntentQuotaStore)
	if !ok {
		return errors.New("pipeline automation intent storage unavailable")
	}
	reservationID := pipelineAutomationReservationID(work.UserID, work.ProjectID, fingerprint, settings.Revision, settings.adminRevision)
	_, reserved, err := autoStore.ReserveQuotaForPipelineAutomation(ctx, reservationID, work.UserID, work.ProjectID,
		d.Handler.pipelineLimits(), time.Now().UTC(), d.Handler.isDemoUser(work.UserID), alreadyRunning,
		newRaw, rawDirty, annotationDirty,
		scopedfirestore.PipelineAutomationAdmission{
			SourceFingerprint: fingerprint, SourceRevision: settings.SourceRevision,
			ProjectRevision: settings.Revision, AdminRevision: settings.adminRevision,
		})
	if err != nil {
		return err
	}
	if !reserved {
		if activeStore, ok := quota.(interface {
			PipelineAdmissionActive(context.Context, string, string) (bool, error)
		}); ok {
			active, activeErr := activeStore.PipelineAdmissionActive(ctx, work.UserID, work.ProjectID)
			if activeErr != nil {
				return activeErr
			}
			if active {
				return errProfileRuntimeWaiting
			}
		}
		return d.completeProjectAutomationBacklog(ctx, repo, work, settings.Revision)
	}
	reservation, exists, err := intentStore.GetPipelineIntent(ctx, work.UserID, work.ProjectID, reservationID)
	if err != nil || !exists {
		if err != nil {
			return err
		}
		return errors.New("pipeline automation reservation was not persisted")
	}
	if err := d.Handler.ensurePipelineIntentWork(ctx, reservation); err != nil {
		return err
	}
	return d.completeProjectAutomationBacklog(ctx, repo, work, settings.Revision)
}

func (d *ProfileDispatcher) completeProjectAutomationBacklog(ctx context.Context, repo *firestoreProfileRepository, work profileruntime.Work, expectedRevision int64) error {
	if !strings.HasPrefix(work.ID, "project-toggle-") {
		return nil
	}
	projectRef := scopedfirestore.Collection(repo.client, "projects").Doc(projectDocID(work.UserID, work.ProjectID))
	return repo.client.RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		snapshot, err := tx.Get(projectRef)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return nil
			}
			return err
		}
		data := snapshot.Data()
		if !projectMatchesOwner(projectRef.ID, data, work.UserID, work.ProjectID) ||
			data["auto_pipeline_enabled"] != true || int64FromAutomationData(data["auto_pipeline_revision"]) != expectedRevision ||
			data["pipeline_backlog_scan_status"] != "pending" {
			return nil
		}
		return tx.Update(projectRef, []cloudfirestore.Update{
			{Path: "pipeline_backlog_scan_status", Value: "complete"},
			{Path: "updated_at", Value: time.Now().UTC()},
		})
	})
}

func pipelineAutomationReservationID(ownerID, projectID, fingerprint string, projectRevision, adminRevision int64) string {
	projectScope := sha256.Sum256([]byte(ownerID + "\x00" + projectID))
	return fmt.Sprintf("auto-%s-%s-%d-%d", hex.EncodeToString(projectScope[:]), fingerprint, projectRevision, adminRevision)
}

func (d *ProfileDispatcher) executePipelineAutomationScan(ctx context.Context, repo *firestoreProfileRepository, work profileruntime.Work) error {
	if d.Handler == nil || repo == nil || repo.client == nil {
		return errors.New("pipeline automation scan storage unavailable")
	}
	fs := repo.client
	adminRef := pipelineAutomationAdminRef(fs)
	scanRef := scopedfirestore.Collection(fs, pipelineAutomationScans).Doc(fmt.Sprint(work.Revision))
	state, err := scanRef.Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil
		}
		return err
	}
	cursor, _ := state.Data()["cursor"].(string)
	query := scopedfirestore.Collection(fs, "projects").OrderBy(cloudfirestore.DocumentID, cloudfirestore.Asc)
	if cursor != "" {
		query = query.StartAfter(cursor)
	}
	docs, err := query.Limit(pipelineAutomationScanPageSize).Documents(ctx).GetAll()
	if err != nil {
		return err
	}
	lastID := cursor
	if len(docs) > 0 {
		lastID = docs[len(docs)-1].Ref.ID
	}
	more := len(docs) == pipelineAutomationScanPageSize
	now := time.Now().UTC()
	return fs.RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		stateSnapshot, err := tx.Get(scanRef)
		if err != nil {
			return err
		}
		adminSnapshot, adminErr := tx.Get(adminRef)
		if adminErr != nil && status.Code(adminErr) != codes.NotFound {
			return adminErr
		}
		admin := pipelineAutomationSetting{backlogStatus: "idle"}
		if adminSnapshot != nil && adminSnapshot.Exists() {
			admin = pipelineAutomationSettingFromData(adminSnapshot.Data())
		}
		stateData := stateSnapshot.Data()
		storedCursor, _ := stateData["cursor"].(string)
		storedStatus, _ := stateData["status"].(string)
		if storedStatus != "pending" || storedCursor != cursor || !admin.allow || admin.revision != work.Revision {
			return nil
		}
		projectData := make([]map[string]interface{}, len(docs))
		for i, doc := range docs {
			current, err := tx.Get(doc.Ref)
			if err != nil {
				if status.Code(err) == codes.NotFound {
					continue
				}
				return err
			}
			projectData[i] = current.Data()
		}
		for i, data := range projectData {
			if data == nil || data["status"] != "ready" || data["auto_pipeline_enabled"] != true {
				continue
			}
			ownerID, _ := data["user_id"].(string)
			projectID, _ := data["project_id"].(string)
			if ownerID == "" || projectID == "" || docs[i].Ref.ID != projectDocID(ownerID, projectID) {
				continue
			}
			revision := int64FromAutomationData(data["pipeline_source_revision"])
			id := fmt.Sprintf("backlog-%d-%d", work.Revision, revision)
			if err := profileruntime.Enqueue(tx, fs, profileruntime.Work{
				UserID: ownerID, ProjectID: projectID, Kind: "pipeline_automation", Revision: revision,
				ID: id, Due: now,
			}); err != nil {
				return err
			}
			if data["pipeline_backlog_scan_status"] == "pending" {
				if err := tx.Update(docs[i].Ref, []cloudfirestore.Update{
					{Path: "pipeline_backlog_scan_status", Value: "complete"}, {Path: "updated_at", Value: now},
				}); err != nil {
					return err
				}
			}
		}
		if more {
			nextID := fmt.Sprintf("scan-%d-%x", work.Revision, sourceautomationCursorHash(lastID))
			if err := profileruntime.Enqueue(tx, fs, profileruntime.Work{
				UserID: "system", ProjectID: "automation", Kind: "pipeline_automation_scan",
				Revision: work.Revision, ID: nextID, Due: now,
			}); err != nil {
				return err
			}
			return tx.Update(scanRef, []cloudfirestore.Update{{Path: "cursor", Value: lastID}, {Path: "updated_at", Value: now}})
		}
		if err := tx.Update(scanRef, []cloudfirestore.Update{{Path: "cursor", Value: lastID}, {Path: "status", Value: "complete"}, {Path: "updated_at", Value: now}}); err != nil {
			return err
		}
		return tx.Update(adminRef, []cloudfirestore.Update{
			{Path: "backlog_scan_status", Value: "complete"}, {Path: "updated_at", Value: now},
		})
	})
}

func int64FromAutomationData(value interface{}) int64 {
	if number, ok := value.(int64); ok {
		return number
	}
	return 0
}

func sourceautomationCursorHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}
