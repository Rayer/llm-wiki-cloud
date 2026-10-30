type Params = Record<string, string | number>;
type Translator = (locale: 'en', key: string, params?: Params) => string;

const englishKeys = new Set([
  'Admin.accessDenied', 'Admin.accessRequired', 'Admin.actionFailed', 'Admin.actions', 'Admin.active', 'Admin.announcement',
  'Admin.announcementHint', 'Admin.announcementMarkdown', 'Admin.announcementPublishFailed', 'Admin.announcementPublished',
  'Admin.cleanRebuild', 'Admin.conceptCount', 'Admin.confirmClearAnnouncement', 'Admin.confirmDeleteProject',
  'Admin.confirmDeleteUser', 'Admin.confirmRebuildIndex', 'Admin.confirmRegenerateQueryChips',
  'Admin.confirmRestoreUser', 'Admin.confirmSuspendUser', 'Admin.confirmTriggerPipeline', 'Admin.currentScopeState', 'Admin.delete', 'Admin.deleteProject', 'Admin.deleteUser',
  'Admin.deleting', 'Admin.grantScope', 'Admin.indexRebuildStarted', 'Admin.manageScopes', 'Admin.managementScopes',
  'Admin.noProjectsFound', 'Admin.noUsersFound', 'Admin.none', 'Admin.owner', 'Admin.pipelineTriggered',
  'Admin.pipelineTriggeredClean', 'Admin.preview', 'Admin.project', 'Admin.projectCount', 'Admin.projectDeleted',
  'Admin.projectId', 'Admin.projectName', 'Admin.projectRenamed', 'Admin.projects', 'Admin.publish',
  'Admin.queryChipsCompleted', 'Admin.queryChipsFailed', 'Admin.queryChipsPending', 'Admin.queryChipsStarted',
  'Admin.rebuildIndex', 'Admin.refresh', 'Admin.regenerateChips', 'Admin.regenerateQueryChips',
  'Admin.registrationUpdated', 'Admin.removeScope', 'Admin.rename', 'Admin.renameFailed', 'Admin.renameProject',
  'Admin.renaming', 'Admin.restore', 'Admin.restoreUser', 'Admin.scopeUpdateFailed', 'Admin.scopeUpdated',
  'Admin.settings', 'Admin.settingsUpdateFailed', 'Admin.sharedLlmTab', 'Admin.sourceCount', 'Admin.starting',
  'Admin.status', 'Admin.statusUnavailable', 'Admin.subtitle', 'Admin.suspend', 'Admin.suspended', 'Admin.suspendUser', 'Admin.title',
  'Admin.triggerPipeline', 'Admin.triggering', 'Admin.unknown', 'Admin.updating', 'Admin.user', 'Admin.userDeleted',
  'Admin.userId', 'Admin.userReadbackFailure', 'Admin.userRestored', 'Admin.userSuspended', 'Admin.users',
  'Common.close', 'Common.noContent', 'Common.noMarkdownContent', 'Command.noMatches', 'Command.title',
  'Detail.entryNotFound', 'MCPOAuth.copySharedInstructions', 'MCPOAuth.sharedInstructions',
  'MCPOAuth.sharedInstructionsCopied', 'MCPOAuth.sharedInstructionsCopyFailed', 'MCPOAuth.sharedInstructionsTitle',
  'ProjectSettings.lastUsed', 'AccountSettings.projectKeyQueryOnly', 'Raw.ingested', 'Raw.name', 'Raw.sha256',
  'Raw.size', 'Raw.updated', 'RawUpload.fileProgress', 'RawUpload.retryFile', 'RawUpload.statusAlreadyExists',
  'RawUpload.statusCreated', 'RawUpload.statusFailed', 'RawUpload.statusQueued', 'RawUpload.statusReplaced',
  'RawUpload.statusUploading', 'RawUpload.summary', 'RuntimeConfig.loading', 'RuntimeConfig.reload',
  'Shell.cancel', 'Shell.loading', 'Shell.retry', 'Shell.scrollToTop', 'States.loadingDetail', 'States.loadingWikiData',
  'Status.childCommand', 'Status.description', 'Status.detailCode', 'Status.duration', 'Status.emptyPipelineLog',
  'Status.errorClass', 'Status.exitCode', 'Status.failedStage', 'Status.failedStageUnavailable', 'Status.finished',
  'Status.idle', 'Status.loading', 'Status.loadingLog', 'Status.loadingPipelineLog', 'Status.noPipelineRuns',
  'Status.openPipelineLog', 'Status.pipelineLog', 'Status.pipelineTimeline', 'Status.pollingPaused',
  'Status.retryRefresh', 'Status.showFullLog', 'Status.showLatestLines', 'Status.started', 'Status.title',
  'Status.stageInputMaterialization', 'Status.stageSyntoMigration', 'Status.stageSyntoConfigNormalization',
  'Status.stageSyntoConfigValidation', 'Status.stageSyntoRun', 'Status.stageSyntoIndexExport',
  'Status.stageSourceReconciliation', 'Status.stageConceptReconciliation', 'Status.stagePostprocess',
  'Status.stageGenerationPublish', 'Status.stageReceiptRecording', 'Status.stageLeaseCleanup',
]);

export function i18nLaneTestTranslate(translate: Translator, key: string, params?: Params): string {
  if (!englishKeys.has(key) || key === 'Admin.registrationEnabled' || key === 'Admin.emailRegistrationEnabled' || key === 'Admin.googleRegistrationEnabled') {
    return key;
  }
  return translate('en', key, params);
}
