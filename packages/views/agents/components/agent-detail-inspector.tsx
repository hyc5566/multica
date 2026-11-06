"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type {
  Agent,
  AgentConversationStarter,
  AgentRuntime,
  MemberWithUser,
} from "@multica/core/types";
import {
  AGENT_DESCRIPTION_MAX_LENGTH,
  AGENT_MAX_CONCURRENT_TASKS_MAX,
  AGENT_MAX_CONCURRENT_TASKS_MIN,
} from "@multica/core/agents";
import { useConfigStore } from "@multica/core/config";
import { isImeComposing } from "@multica/core/utils";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { cn } from "@multica/ui/lib/utils";
import { Button } from "@multica/ui/components/ui/button";
import { Loader2 } from "@multica/ui/icons";
import { AvatarUploadControl } from "../../common/avatar-upload-control";
import {
  SettingsCard,
  SettingsRow,
  SettingsSaveState,
  SettingsSection,
} from "../../settings/components/settings-layout";
import { useAutoSave } from "../../settings/components/use-auto-save";
import { useT } from "../../i18n";
import { CharCounter } from "./char-counter";
import { ConversationStartersEditor } from "./conversation-starters-editor";
import { NewChatPreview } from "./new-chat-preview";
import { useConversationStartersFocus } from "./use-conversation-starters-focus";
import { ModelPicker } from "./inspector/model-picker";
import { RuntimePicker } from "./inspector/runtime-picker";
import { ThinkingSettingField } from "./inspector/thinking-prop-row";
import { ServiceTierSettingField } from "./inspector/service-tier-setting-field";
import { useExecutionControls } from "./inspector/use-execution-controls";

interface InspectorProps {
  agent: Agent;
  runtime: AgentRuntime | null;
  runtimes: AgentRuntime[];
  members: MemberWithUser[];
  currentUserId: string | null;
  canEdit: boolean;
  onUpdate: (id: string, data: Record<string, unknown>) => Promise<void>;
}

interface ProfileDraft {
  name: string;
  description: string;
}

function profileDraftsEqual(left: ProfileDraft, right: ProfileDraft) {
  return left.name === right.name && left.description === right.description;
}

function startersEqual(
  left: AgentConversationStarter[],
  right: AgentConversationStarter[],
) {
  return JSON.stringify(left) === JSON.stringify(right);
}

/**
 * Full-width General settings form. Every editable value is presented as an
 * explicit field; compact inspector chips are used only through their
 * settings-field variants, where the whole control is a visible click target.
 */
export function AgentDetailInspector({
  agent,
  runtime,
  runtimes,
  members,
  currentUserId,
  canEdit,
  onUpdate,
}: InspectorProps) {
  const { t } = useT("agents");
  const { t: ts } = useT("settings");
  const update = useCallback(
    (data: Record<string, unknown>) => onUpdate(agent.id, data),
    [agent.id, onUpdate],
  );

  const [name, setName] = useState(agent.name);
  const [description, setDescription] = useState(agent.description ?? "");
  const [savingProfile, setSavingProfile] = useState(false);
  const savedProfileRef = useRef({
    agentId: agent.id,
    name: agent.name,
    description: agent.description ?? "",
  });
  const profileDraftRef = useRef({
    name: agent.name,
    description: agent.description ?? "",
  });
  profileDraftRef.current = { name, description };

  useEffect(() => {
    const incoming = {
      agentId: agent.id,
      name: agent.name,
      description: agent.description ?? "",
    };
    const saved = savedProfileRef.current;
    const switchingAgents = saved.agentId !== agent.id;
    const localDirty =
      profileDraftRef.current.name !== saved.name ||
      profileDraftRef.current.description !== saved.description;
    const serverChanged =
      incoming.name !== saved.name || incoming.description !== saved.description;
    if (switchingAgents || (!savingProfile && !localDirty && serverChanged)) {
      savedProfileRef.current = incoming;
      setName(incoming.name);
      setDescription(incoming.description);
    }
  }, [agent.description, agent.id, agent.name, savingProfile]);

  const profileDraft = useMemo(
    () => ({ name: name.trim(), description }),
    [description, name],
  );
  const savedProfile = savedProfileRef.current;
  const profileDirty = !profileDraftsEqual(profileDraft, savedProfile);
  const saveProfile = async () => {
    const next = profileDraft;
    setSavingProfile(true);
    try {
      await update({ name: next.name, description: next.description });
      savedProfileRef.current = { agentId: agent.id, ...next };
      setName(next.name);
    } catch {
      // The parent owns the error toast; keep the draft available for retry.
    } finally {
      setSavingProfile(false);
    }
  };
  const discardProfile = () => {
    setName(savedProfileRef.current.name);
    setDescription(savedProfileRef.current.description);
  };

  const [starters, setStarters] = useState<AgentConversationStarter[]>(
    agent.conversation_starters ?? [],
  );
  useEffect(() => {
    setStarters(agent.conversation_starters ?? []);
    // Cache updates must not erase a newer draft while its autosave is in flight.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agent.id]);

  const {
    qoderRuntime,
    canDiscoverRuntimeModels,
    runtimeOptions,
    changeModel,
    changeRuntime,
  } = useExecutionControls({ agent, runtime, runtimes, currentUserId, update });
  const conversationStartersEnabled = useConfigStore(
    (state) => state.agentConversationStartersSupported,
  );
  const showStarters = conversationStartersEnabled;
  const startersComplete = starters.every(
    (item) => item.label.trim() && item.prompt.trim(),
  );
  const savedStarters = useMemo(
    () => agent.conversation_starters ?? [],
    [agent.conversation_starters],
  );
  const saveStarters = useCallback(
    async (next: AgentConversationStarter[]) => {
      await update({ conversation_starters: next });
    },
    [update],
  );
  const startersAutoSave = useAutoSave({
    value: starters,
    savedValue: savedStarters,
    onSave: saveStarters,
    enabled: canEdit && showStarters && startersComplete,
    isEqual: startersEqual,
  });
  const startersFocus = useConversationStartersFocus(agent.id, showStarters);
  const nameInvalid = name.trim().length === 0;

  return (
    <div className="@container">
      <div className="@4xl:grid @4xl:grid-cols-[minmax(0,1fr)_17.5rem] @4xl:items-start @4xl:gap-8">
        <div className="min-w-0 space-y-8">
          <SettingsSection
            title={t(($) => $.inspector.section_profile)}
          >
            <SettingsCard>
              <SettingsRow
                label={t(($) => $.inspector.avatar_label)}
                size="none"
              >
                <div className="flex justify-start sm:justify-end">
                  <AvatarUploadControl
                    variant="agent"
                    value={agent.avatar_url ?? null}
                    name={agent.name}
                    size={56}
                    disabled={!canEdit}
                    onUploaded={(url) => update({ avatar_url: url })}
                    onEmojiSelected={(value) => update({ avatar_url: value })}
                  />
                </div>
              </SettingsRow>

              <SettingsRow
                label={t(($) => $.inspector.name_label)}
                size="text"
              >
                <div>
                  <Input
                    type="text"
                    name="agent-name"
                    autoComplete="off"
                    aria-label={t(($) => $.inspector.name_label)}
                    value={name}
                    onChange={(event) => setName(event.target.value)}
                    disabled={!canEdit || savingProfile}
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
                    value={description}
                    onChange={(event) => setDescription(event.target.value)}
                    disabled={!canEdit || savingProfile}
                    rows={5}
                    maxLength={AGENT_DESCRIPTION_MAX_LENGTH}
                    className="resize-y"
                    placeholder={t(($) => $.inspector.description_placeholder)}
                  />
                  <CharCounter
                    length={[...description].length}
                    max={AGENT_DESCRIPTION_MAX_LENGTH}
                  />
                </div>
              </SettingsRow>

              {showStarters ? (
                <div
                  ref={startersFocus.ref}
                  className={cn(
                    "scroll-mt-4 transition-shadow duration-500",
                    startersFocus.focused && "ring-2 ring-inset ring-brand/50",
                  )}
                >
                  <SettingsRow
                    label={t(($) => $.conversation_starters.label)}
                    description={t(($) => $.conversation_starters.hint)}
                    size="text"
                    align="start"
                  >
                    <div className="space-y-2">
                      <ConversationStartersEditor
                        value={starters}
                        onChange={setStarters}
                        disabled={!canEdit}
                      />
                      <SettingsSaveState
                        status={startersAutoSave.status}
                        savingLabel={ts(($) => $.auto_save.saving)}
                        savedLabel={ts(($) => $.auto_save.saved)}
                        errorLabel={ts(($) => $.auto_save.failed)}
                      />
                    </div>
                  </SettingsRow>
                </div>
              ) : null}
            </SettingsCard>
            {profileDirty && canEdit && (
              <div className="flex items-center justify-end gap-2">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={discardProfile}
                  disabled={savingProfile}
                >
                  {t(($) => $.inspector.discard_changes)}
                </Button>
                <Button
                  type="button"
                  size="sm"
                  onClick={saveProfile}
                  aria-busy={savingProfile}
                  disabled={
                    savingProfile ||
                    profileDraft.name.length === 0 ||
                    profileDraft.description.length > AGENT_DESCRIPTION_MAX_LENGTH
                  }
                >
                  {savingProfile && (
                    <Loader2 className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />
                  )}
                  {t(($) => $.inspector.save_changes)}
                </Button>
              </div>
            )}
          </SettingsSection>

          <SettingsSection
            title={t(($) => $.inspector.section_execution)}
            description={
              qoderRuntime ? t(($) => $.inspector.qoder_execution_note) : undefined
            }
          >
            <SettingsCard>
              <SettingsRow
                label={t(($) => $.inspector.prop_runtime)}
                size="select-wide"
              >
                <RuntimePicker
                  variant="field"
                  showLabel={false}
                  value={agent.runtime_id}
                  runtimes={runtimeOptions}
                  members={members}
                  currentUserId={currentUserId}
                  canEdit={canEdit && !qoderRuntime}
                  onChange={changeRuntime}
                />
              </SettingsRow>
              {qoderRuntime ? null : (
                <>
                  <SettingsRow
                    label={t(($) => $.inspector.prop_model)}
                    size="select-wide"
                  >
                    <ModelPicker
                      variant="field"
                      showLabel={false}
                      runtimeId={agent.runtime_id}
                      runtimeOnline={canDiscoverRuntimeModels}
                      value={agent.model ?? ""}
                      canEdit={canEdit}
                      onChange={changeModel}
                    />
                  </SettingsRow>
                  <ThinkingSettingField
                    label={t(($) => $.inspector.prop_thinking)}
                    runtimeId={agent.runtime_id}
                    runtimeOnline={canDiscoverRuntimeModels}
                    provider={runtime?.provider ?? ""}
                    model={agent.model ?? ""}
                    value={agent.thinking_level ?? ""}
                    canEdit={canEdit}
                    onChange={(thinkingLevel) =>
                      update({ thinking_level: thinkingLevel })
                    }
                  />
                  <ServiceTierSettingField
                    label={t(($) => $.inspector.prop_speed)}
                    runtimeId={agent.runtime_id}
                    runtimeOnline={canDiscoverRuntimeModels}
                    provider={runtime?.provider ?? ""}
                    model={agent.model ?? ""}
                    value={agent.service_tier ?? ""}
                    canEdit={canEdit}
                    onChange={(serviceTier) =>
                      update({ service_tier: serviceTier })
                    }
                  />
                </>
              )}
              <SettingsRow
                label={t(($) => $.inspector.prop_concurrency)}
                size="select-wide"
              >
                <ConcurrencyField
                  value={agent.max_concurrent_tasks}
                  canEdit={canEdit}
                  onSave={(next) => update({ max_concurrent_tasks: next })}
                />
              </SettingsRow>
            </SettingsCard>
          </SettingsSection>
        </div>

        <SettingsSection
          title={t(($) => $.conversation_starters.preview_label)}
          className="hidden @4xl:sticky @4xl:top-0 @4xl:block"
        >
          <NewChatPreview
            name={name.trim() || agent.name}
            description={description}
            avatarUrl={agent.avatar_url ?? null}
            starters={showStarters ? starters : []}
          />
        </SettingsSection>
      </div>
    </div>
  );
}

function ConcurrencyField({
  value,
  canEdit,
  onSave,
}: {
  value: number;
  canEdit: boolean;
  onSave: (next: number) => Promise<void>;
}) {
  const { t } = useT("agents");
  const [draft, setDraft] = useState(String(value));

  useEffect(() => setDraft(String(value)), [value]);

  const commit = () => {
    const next = Number(draft);
    if (
      !Number.isInteger(next) ||
      next < AGENT_MAX_CONCURRENT_TASKS_MIN ||
      next > AGENT_MAX_CONCURRENT_TASKS_MAX
    ) {
      setDraft(String(value));
      return;
    }
    if (next !== value) void onSave(next);
  };

  return (
    <div>
      <Input
        id="agent-concurrency"
        type="number"
        name="agent-concurrency"
        autoComplete="off"
        inputMode="numeric"
        min={AGENT_MAX_CONCURRENT_TASKS_MIN}
        max={AGENT_MAX_CONCURRENT_TASKS_MAX}
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
        onBlur={commit}
        onKeyDown={(event) => {
          if (isImeComposing(event)) return;
          if (event.key === "Enter") {
            event.preventDefault();
            commit();
          }
        }}
        disabled={!canEdit}
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
  );
}
