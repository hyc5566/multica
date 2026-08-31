"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type {
  Agent,
  AgentRuntime,
  MemberWithUser,
  UpdateAgentRequest,
} from "@multica/core/types";
import {
  AGENT_DESCRIPTION_MAX_LENGTH,
  AGENT_MAX_CONCURRENT_TASKS_MAX,
  AGENT_MAX_CONCURRENT_TASKS_MIN,
} from "@multica/core/agents";
import {
  isRuntimeUsableForUser,
  runtimeModelsOptions,
} from "@multica/core/runtimes";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { AvatarUploadControl } from "../../common/avatar-upload-control";
import {
  SettingsCard,
  SettingsRow,
  SettingsSection,
} from "../../settings/components/settings-layout";
import { useT } from "../../i18n";
import { CharCounter } from "./char-counter";
import { DirtyFormActions } from "./dirty-form-actions";
import { ModelPicker } from "./inspector/model-picker";
import {
  buildModelChangeUpdate,
  type ModelCatalog,
} from "./inspector/model-change-cleanup";
import { RuntimePicker } from "./inspector/runtime-picker";
import { ThinkingSettingField } from "./inspector/thinking-prop-row";
import { ServiceTierSettingField } from "./inspector/service-tier-setting-field";

interface InspectorProps {
  agent: Agent;
  runtime: AgentRuntime | null;
  runtimes: AgentRuntime[];
  members: MemberWithUser[];
  currentUserId: string | null;
  canEdit: boolean;
  onDirtyChange?: (dirty: boolean) => void;
  onUpdate: (id: string, data: Record<string, unknown>) => Promise<void>;
}

interface AgentSettingsDraft {
  name: string;
  description: string;
  runtimeId: string;
  model: string;
  thinkingLevel: string;
  serviceTier: string;
  maxConcurrentTasks: string;
}

function draftsEqual(left: AgentSettingsDraft, right: AgentSettingsDraft) {
  return (
    left.name === right.name &&
    left.description === right.description &&
    left.runtimeId === right.runtimeId &&
    left.model === right.model &&
    left.thinkingLevel === right.thinkingLevel &&
    left.serviceTier === right.serviceTier &&
    left.maxConcurrentTasks === right.maxConcurrentTasks
  );
}

function buildSettingsUpdate(
  draft: AgentSettingsDraft,
  baseline: AgentSettingsDraft,
): UpdateAgentRequest {
  const update: UpdateAgentRequest = {};
  if (draft.name.trim() !== baseline.name) update.name = draft.name.trim();
  if (draft.description !== baseline.description) {
    update.description = draft.description;
  }
  if (draft.runtimeId !== baseline.runtimeId) update.runtime_id = draft.runtimeId;
  if (draft.model !== baseline.model) update.model = draft.model;
  if (draft.thinkingLevel !== baseline.thinkingLevel) {
    update.thinking_level = draft.thinkingLevel;
  }
  if (draft.serviceTier !== baseline.serviceTier) {
    update.service_tier = draft.serviceTier;
  }
  if (draft.maxConcurrentTasks !== baseline.maxConcurrentTasks) {
    update.max_concurrent_tasks = Number(draft.maxConcurrentTasks);
  }
  return update;
}

/**
 * General agent settings are one draft. Field changes stay local until the
 * user explicitly saves; Reset restores the last successful server snapshot.
 */
export function AgentDetailInspector({
  agent,
  runtime,
  runtimes,
  members,
  currentUserId,
  canEdit,
  onDirtyChange,
  onUpdate,
}: InspectorProps) {
  const { t } = useT("agents");
  const incomingDraft = useMemo(
    () => ({
      name: agent.name,
      description: agent.description ?? "",
      runtimeId: agent.runtime_id ?? "",
      model: agent.model ?? "",
      thinkingLevel: agent.thinking_level ?? "",
      serviceTier: agent.service_tier ?? "",
      maxConcurrentTasks: String(agent.max_concurrent_tasks),
    }),
    [
      agent.description,
      agent.max_concurrent_tasks,
      agent.model,
      agent.name,
      agent.runtime_id,
      agent.service_tier,
      agent.thinking_level,
    ],
  );
  const [baseline, setBaseline] = useState(incomingDraft);
  const [draft, setDraft] = useState(incomingDraft);
  const [saving, setSaving] = useState(false);
  const activeAgentIdRef = useRef(agent.id);
  const dirty = !draftsEqual(draft, baseline);

  useEffect(() => {
    const switchingAgents = activeAgentIdRef.current !== agent.id;
    if (switchingAgents) {
      activeAgentIdRef.current = agent.id;
      setBaseline(incomingDraft);
      setDraft(incomingDraft);
      return;
    }
    if (!dirty && !saving && !draftsEqual(incomingDraft, baseline)) {
      setBaseline(incomingDraft);
      setDraft(incomingDraft);
    }
  }, [agent.id, baseline, dirty, incomingDraft, saving]);

  useEffect(() => {
    onDirtyChange?.(dirty);
    return () => onDirtyChange?.(false);
  }, [dirty, onDirtyChange]);

  const draftRuntime =
    runtimes.find((candidate) => candidate.id === draft.runtimeId) ??
    (runtime?.id === draft.runtimeId ? runtime : null);
  const canReadRuntime =
    draftRuntime != null &&
    isRuntimeUsableForUser(draftRuntime, currentUserId);
  const canDiscoverRuntimeModels =
    draftRuntime?.status === "online" && canReadRuntime;
  const modelsQuery = useQuery(
    runtimeModelsOptions(canDiscoverRuntimeModels ? draft.runtimeId : null),
  );
  const modelCatalog = useMemo<ModelCatalog>(
    () =>
      modelsQuery.isSuccess
        ? modelsQuery.data.supported
          ? modelsQuery.data.models
          : []
        : null,
    [modelsQuery.data, modelsQuery.isSuccess],
  );

  const concurrency = Number(draft.maxConcurrentTasks);
  const nameInvalid = draft.name.trim().length === 0;
  const descriptionInvalid =
    [...draft.description].length > AGENT_DESCRIPTION_MAX_LENGTH;
  const concurrencyInvalid =
    !Number.isInteger(concurrency) ||
    concurrency < AGENT_MAX_CONCURRENT_TASKS_MIN ||
    concurrency > AGENT_MAX_CONCURRENT_TASKS_MAX;
  const formInvalid = nameInvalid || descriptionInvalid || concurrencyInvalid;
  const controlsDisabled = !canEdit || saving;

  const handleModelChange = useCallback(
    (model: string) => {
      setDraft((current) => {
        const update = buildModelChangeUpdate({
          provider: draftRuntime?.provider ?? "",
          model,
          thinkingLevel: current.thinkingLevel,
          serviceTier: current.serviceTier,
          catalog: modelCatalog,
        });
        return {
          ...current,
          model: update.model,
          thinkingLevel:
            update.thinking_level === undefined
              ? current.thinkingLevel
              : update.thinking_level,
          serviceTier:
            update.service_tier === undefined
              ? current.serviceTier
              : update.service_tier,
        };
      });
    },
    [draftRuntime?.provider, modelCatalog],
  );

  const reset = () => setDraft(baseline);
  const save = async () => {
    if (!dirty || formInvalid || saving) return;
    const submitted = {
      ...draft,
      name: draft.name.trim(),
      maxConcurrentTasks: String(concurrency),
    };
    const update = buildSettingsUpdate(submitted, baseline);
    setSaving(true);
    try {
      await onUpdate(agent.id, update as Record<string, unknown>);
      setBaseline(submitted);
      setDraft(submitted);
    } catch {
      // Parent owns the toast. Keep the draft intact for retry or reset.
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="space-y-8">
      <SettingsSection
        title={t(($) => $.inspector.section_profile)}
        description={t(($) => $.inspector.section_profile_hint)}
      >
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.inspector.avatar_label)}
            description={t(($) => $.inspector.avatar_hint)}
            size="none"
          >
            <div className="flex justify-start sm:justify-end">
              <AvatarUploadControl
                variant="agent"
                value={agent.avatar_url ?? null}
                name={draft.name || agent.name}
                size={56}
                disabled={controlsDisabled}
                onUploaded={(url) => onUpdate(agent.id, { avatar_url: url })}
                onEmojiSelected={(value) =>
                  onUpdate(agent.id, { avatar_url: value })
                }
              />
            </div>
          </SettingsRow>

          <SettingsRow label={t(($) => $.inspector.name_label)} size="text">
            <div>
              <Input
                type="text"
                name="agent-name"
                autoComplete="off"
                aria-label={t(($) => $.inspector.name_label)}
                value={draft.name}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    name: event.target.value,
                  }))
                }
                disabled={controlsDisabled}
                aria-invalid={nameInvalid || undefined}
              />
              {nameInvalid ? (
                <p className="mt-1 text-caption text-destructive">
                  {t(($) => $.inspector.rename_required)}
                </p>
              ) : null}
            </div>
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.inspector.description_label)}
            size="text"
            align="start"
          >
            <div>
              <Textarea
                name="agent-description"
                autoComplete="off"
                aria-label={t(($) => $.inspector.description_label)}
                value={draft.description}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    description: event.target.value,
                  }))
                }
                disabled={controlsDisabled}
                rows={5}
                maxLength={AGENT_DESCRIPTION_MAX_LENGTH}
                className="resize-y"
                placeholder={t(($) => $.inspector.description_placeholder)}
              />
              <CharCounter
                length={[...draft.description].length}
                max={AGENT_DESCRIPTION_MAX_LENGTH}
              />
            </div>
          </SettingsRow>
        </SettingsCard>
      </SettingsSection>

      <SettingsSection
        title={t(($) => $.inspector.section_execution)}
        description={t(($) => $.inspector.section_execution_hint)}
      >
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.inspector.prop_runtime)}
            size="select-wide"
          >
            <RuntimePicker
              variant="field"
              showLabel={false}
              value={draft.runtimeId}
              runtimes={runtimes}
              members={members}
              currentUserId={currentUserId}
              canEdit={!controlsDisabled}
              onChange={(runtimeId) =>
                setDraft((current) => ({
                  ...current,
                  runtimeId,
                  model: "",
                  thinkingLevel: "",
                  serviceTier: "",
                }))
              }
            />
          </SettingsRow>
          <SettingsRow
            label={t(($) => $.inspector.prop_model)}
            size="select-wide"
          >
            <ModelPicker
              variant="field"
              showLabel={false}
              runtimeId={draft.runtimeId}
              runtimeOnline={canDiscoverRuntimeModels}
              value={draft.model}
              canEdit={!controlsDisabled}
              onChange={handleModelChange}
            />
          </SettingsRow>
          <ThinkingSettingField
            label={t(($) => $.inspector.prop_thinking)}
            runtimeId={draft.runtimeId}
            runtimeOnline={canDiscoverRuntimeModels}
            provider={draftRuntime?.provider ?? ""}
            model={draft.model}
            value={draft.thinkingLevel}
            canEdit={!controlsDisabled}
            onChange={(thinkingLevel) =>
              setDraft((current) => ({ ...current, thinkingLevel }))
            }
          />
          <ServiceTierSettingField
            label={t(($) => $.inspector.prop_speed)}
            runtimeId={draft.runtimeId}
            runtimeOnline={canDiscoverRuntimeModels}
            provider={draftRuntime?.provider ?? ""}
            model={draft.model}
            value={draft.serviceTier}
            canEdit={!controlsDisabled}
            onChange={(serviceTier) =>
              setDraft((current) => ({ ...current, serviceTier }))
            }
          />
          <SettingsRow
            label={t(($) => $.inspector.prop_concurrency)}
            size="select-wide"
          >
            <div>
              <Input
                id="agent-concurrency"
                type="number"
                name="agent-concurrency"
                autoComplete="off"
                inputMode="numeric"
                min={AGENT_MAX_CONCURRENT_TASKS_MIN}
                max={AGENT_MAX_CONCURRENT_TASKS_MAX}
                value={draft.maxConcurrentTasks}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    maxConcurrentTasks: event.target.value,
                  }))
                }
                disabled={controlsDisabled}
                aria-invalid={concurrencyInvalid || undefined}
                aria-label={t(($) => $.inspector.prop_concurrency)}
                className="font-mono tabular-nums"
              />
              <p className="mt-1 text-caption text-muted-foreground">
                {t(($) => $.pickers.concurrency_range, {
                  min: AGENT_MAX_CONCURRENT_TASKS_MIN,
                  max: AGENT_MAX_CONCURRENT_TASKS_MAX,
                })}
              </p>
            </div>
          </SettingsRow>
        </SettingsCard>
      </SettingsSection>

      <DirtyFormActions
        dirty={dirty}
        saving={saving}
        saveDisabled={formInvalid}
        onReset={reset}
        onSave={() => void save()}
      />
    </div>
  );
}
